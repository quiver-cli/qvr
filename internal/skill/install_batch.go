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
// disk. Resolution is done serially (it touches the shared registry index cache,
// whose writer isn't safe for concurrent same-registry builds); only the file
// materialization — the expensive, fully-independent part — fans out.
type materializeJob struct {
	repoPath     string
	commit       string
	subpath      string
	rootCoexists bool
	finalPath    string
	stagingPath  string
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
// materialization job. It returns ok=false when the content dir already exists
// or resolution fails — both mean "leave it to the serial Install". idx makes
// the staging path unique within the batch so two requests for the same content
// dir (e.g. two aliases of one skill at one SHA) can't collide on staging.
func (in *Installer) planMaterialize(req InstallRequest, idx int) (materializeJob, bool) {
	plan, err := in.resolveInstall(&req)
	if err != nil {
		return materializeJob{}, false
	}
	if _, err := os.Stat(plan.finalPath); err == nil {
		return materializeJob{}, false // already materialized — nothing to do
	}
	return materializeJob{
		repoPath:     plan.loc.RepoPath,
		commit:       plan.resolvedSHA,
		subpath:      plan.loc.Entry.Path,
		rootCoexists: plan.rootCoexists,
		finalPath:    plan.finalPath,
		// Unique per job AND per process: parallel workers within this batch,
		// and a second concurrent `qvr add` process, never share a staging dir.
		stagingPath: fmt.Sprintf("%s.staging.%d.%d", plan.finalPath, os.Getpid(), idx),
	}, true
}

// runMaterializeJob writes one content tree into its staging dir and atomically
// renames it into place. Any failure leaves no partial final dir and is silently
// dropped — Install will redo it. A lost rename race (another worker/process
// created finalPath first) is success: the winning dir is content-identical.
func (in *Installer) runMaterializeJob(j materializeJob) {
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
