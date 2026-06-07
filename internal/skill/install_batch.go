package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/astra-sh/qvr/internal/model"
)

// materializeJob is one resolved skill ready to have its content tree written to
// disk and its trust caches warmed. Resolution is done serially (it touches the
// shared registry index cache, whose writer isn't safe for concurrent
// same-registry builds); only the file materialization and cache warming — the
// expensive, fully-independent parts — fan out.
//
// needsMaterialize is false when the SHA-keyed content dir already exists (a
// sibling project on the same commit materialized it). The job is still kept so
// its identity/provenance caches get warmed on THIS machine — a content dir can
// be shared across projects while a fresh checkout's local caches are cold.
type materializeJob struct {
	repoPath         string
	commit           string
	ref              string // resolved human ref label (plan.version) — the provenance cache key
	subpath          string
	rootCoexists     bool
	finalPath        string
	stagingPath      string
	needsMaterialize bool
	requireSigned    bool // mirror the serial install's fresh-provenance gate
	frozen           bool // --frozen: identity recomputes fresh, cache untouched
}

// PrematerializeBatch materializes the content dirs for a batch of installs
// concurrently (#206). It is a BEST-EFFORT optimization with no lock, symlink,
// or scan side effects: it only pre-creates the shared SHA-keyed content dirs
// that Install would otherwise build one-at-a-time. A subsequent serial Install
// per request reuses the pre-built dir via its "finalPath exists → reuse" fast
// path, or — if prematerialization skipped/failed for any reason — does the work
// itself. Because the content dir is content-addressed (registry + canonical
// name + commit SHA) and the scope decision is deterministic, a pre-built dir is
// always a valid input to Install; correctness never depends on this running.
//
// Resolution runs serially (cache-safe); materialization fans out over a bounded
// worker pool. Errors are intentionally swallowed — Install is the source of
// truth and will surface any real failure with its full diagnostics.
func (in *Installer) PrematerializeBatch(reqs []InstallRequest) {
	if len(reqs) < 2 {
		return // nothing to parallelize
	}

	// Cheap warm-cache filter (hot-path fix): drop requests already installed
	// with an on-disk content dir BEFORE doing any registry resolution. On a
	// repeat install every skill is cached, so resolving each one just to learn
	// its content dir already exists would double the resolution cost the serial
	// Install loop pays anyway. A lock peek + stat is far cheaper than
	// resolveSkill + ResolveRef. Items not yet installed (cold) fall through.
	candidates := make([]InstallRequest, 0, len(reqs))
	lock := readBatchLock(reqs[0])
	for _, req := range reqs {
		if contentDirInstalled(lock, req) {
			continue
		}
		candidates = append(candidates, req)
	}
	if len(candidates) < 2 {
		return // 0 or 1 to do — no parallelism to gain; serial Install handles it
	}

	// Phase 1 (serial): resolve each candidate to a materialization job. Skips
	// requests whose content dir already exists or that don't resolve cleanly.
	jobs := make([]materializeJob, 0, len(candidates))
	for i, req := range candidates {
		if job, ok := in.planMaterialize(req, i); ok {
			jobs = append(jobs, job)
		}
	}
	if len(jobs) == 0 {
		return
	}

	// Phase 2 (parallel): write each content tree. Bounded so a huge batch
	// doesn't spawn unbounded goroutines / fds.
	limit := len(jobs)
	if n := runtime.GOMAXPROCS(0); n < limit {
		limit = n
	}
	if limit > 8 {
		limit = 8
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for _, job := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(j materializeJob) {
			defer wg.Done()
			defer func() { <-sem }()
			in.runMaterializeJob(j)
		}(job)
	}
	wg.Wait()
}

// readBatchLock loads the project lock for a batch (all requests share one lock
// path). Returns nil when there's no readable lock — every request is then
// treated as not-yet-installed, the correct default for a fresh project.
func readBatchLock(req InstallRequest) *model.LockFile {
	lp := req.LockPath
	if lp == "" {
		lp = model.DefaultLockPath(req.ProjectRoot, quiverHome(), req.Global)
	}
	lock, err := model.ReadLockFile(lp)
	if err != nil {
		return nil
	}
	return lock
}

// contentDirInstalled reports whether req is already installed with its content
// dir present on disk — i.e. nothing to prematerialize. It's a cheap check (a
// lock lookup + a stat) that deliberately avoids registry resolution, so the
// warm path doesn't pay for resolving skills that are already materialized.
func contentDirInstalled(lock *model.LockFile, req InstallRequest) bool {
	if lock == nil {
		return false
	}
	name, _, err := ParseReference(req.Skill)
	if err != nil {
		return false
	}
	localName := name
	if req.As != "" {
		localName = req.As
	}
	entry, err := lock.Get(localName)
	if err != nil {
		return false
	}
	dir := EntryWorktreePath(entry)
	if dir == "" {
		return false
	}
	_, statErr := os.Stat(filepath.Join(dir, entry.Path, "SKILL.md"))
	return statErr == nil
}

// planMaterialize resolves req via the shared resolveInstall helper (the same
// resolution Install uses, so the two can never drift) and turns it into a
// job. It returns ok=false only when resolution fails — "leave it to the serial
// Install". When the content dir already exists the job is still returned with
// needsMaterialize=false so its trust caches get warmed (see materializeJob).
// idx makes the staging path unique within the batch so two requests for the
// same content dir (e.g. two aliases of one skill at one SHA) can't collide.
func (in *Installer) planMaterialize(req InstallRequest, idx int) (materializeJob, bool) {
	plan, err := in.resolveInstallMemo(&req)
	if err != nil {
		return materializeJob{}, false
	}
	_, statErr := os.Stat(plan.finalPath)
	return materializeJob{
		repoPath:         plan.loc.RepoPath,
		commit:           plan.resolvedSHA,
		ref:              plan.version,
		subpath:          plan.loc.Entry.Path,
		rootCoexists:     plan.rootCoexists,
		finalPath:        plan.finalPath,
		needsMaterialize: statErr != nil,
		requireSigned:    req.RequireSigned,
		frozen:           req.Frozen,
		// Unique per job AND per process: parallel workers within this batch,
		// and a second concurrent `qvr add` process, never share a staging dir.
		stagingPath: fmt.Sprintf("%s.staging.%d.%d", plan.finalPath, os.Getpid(), idx),
	}, true
}

// runMaterializeJob materializes the content tree (when not already on disk)
// then warms the trust caches the serial Install loop will read. Any failure
// leaves no partial final dir and is silently dropped — Install redoes it. A
// lost rename race (another worker/process created finalPath first) is success:
// the winning dir is content-identical.
func (in *Installer) runMaterializeJob(j materializeJob) {
	if j.needsMaterialize {
		_ = os.RemoveAll(j.stagingPath)
		mat := &Materializer{Blob: in.Blob}
		if _, err := mat.MaterializeSubtree(j.repoPath, j.commit, j.subpath, j.rootCoexists, j.stagingPath); err != nil {
			_ = os.RemoveAll(j.stagingPath)
			return
		}
		// Don't promote a dir that lacks the skill's own SKILL.md — Install would
		// reject it anyway, and reusing it would mask the real "absent at ref" error.
		if _, statErr := os.Stat(filepath.Join(j.stagingPath, j.subpath, "SKILL.md")); errors.Is(statErr, os.ErrNotExist) {
			_ = os.RemoveAll(j.stagingPath)
			return
		}
		if err := os.MkdirAll(filepath.Dir(j.finalPath), 0o755); err != nil {
			_ = os.RemoveAll(j.stagingPath)
			return
		}
		if err := os.Rename(j.stagingPath, j.finalPath); err != nil {
			_ = os.RemoveAll(j.stagingPath) // lost the race or a real error; Install recovers
		}
	}
	in.warmInstallCaches(j)
}

// warmInstallCaches populates the global identity + provenance caches for a
// resolved job, mirroring exactly what the serial Install computes and caches —
// the identity SHA-256 walk and up to four `git` subprocesses per skill that
// otherwise run serially in the lock window and dominate a cold multi-skill add.
// It is best-effort and side-effect-free w.r.t. the lock/symlinks/scan: Install
// still re-reads and re-validates everything, so this only pre-populates caches
// it already trusts on the warm path. Both writers are concurrency-safe
// (content-determined bytes via temp-file+rename), and the read helpers
// short-circuit a cache hit, so a fully-warm run is cheap.
func (in *Installer) warmInstallCaches(j materializeJob) {
	// Identity: skip under --frozen — the frozen drift gate recomputes fresh and
	// must not trust a memo (matches Install's useCache = !Frozen).
	if !j.frozen {
		_, _ = subtreeIdentity(j.repoPath, j.commit, j.subpath, j.rootCoexists, true)
	}
	// Provenance + author: skip when the install will recompute fresh — policy
	// requires a signature, or the skill declares signed_by. Those are
	// keyring-dependent and never served from cache (mirrors Install's
	// freshProvenance gate at installer.go). signed_by lives in the skill's
	// materialized SKILL.md, so the content dir must exist to read it.
	skillDir := filepath.Join(j.finalPath, j.subpath)
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
		return
	}
	if j.requireSigned || DeclaredSignedBy(skillDir) != "" {
		return
	}
	_, _ = in.resolveProvenanceAndAuthor(j.repoPath, j.ref, j.commit, j.subpath, false)
}
