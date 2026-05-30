package skill

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	gogitcfg "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/raks097/quiver/internal/canonical"
	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/model"
)

var (
	ErrPublishNoEdit       = errors.New("skill must be ejected with `qvr edit` before publishing")
	ErrPublishNoSource     = errors.New("skill entry has no Source URL — set --fork <url> to publish to a new remote")
	ErrPublishForkRequired = errors.New("--fork <url> requires an ejected skill — run `qvr edit` first")
)

// PublishInstalledRequest drives publishing an already-installed (and
// typically ejected) skill back to git. The flow is the inverse of
// `qvr add`: we work directly off the local edit dir's git repo, push
// to either the entry's recorded Source or a freshly-supplied --fork
// URL, and (optionally) tag.
type PublishInstalledRequest struct {
	Entry       *model.LockEntry
	ProjectRoot string
	// ForkURL re-targets the publish to a new remote. When set, the
	// initial commit is stamped with `forked-from: <SourceUpstream>@<sha>`
	// in SKILL.md frontmatter, origin is rewritten to ForkURL, and the
	// push lands there. Migrate=true also rewrites entry.Source so future
	// publishes track the fork. Migrate also clears entry.Registry (issue
	// #85) because the fork is registry-independent.
	ForkURL string
	Migrate bool
	// Tag, when non-empty, creates an annotated tag at the new commit
	// (pushed atomically with the branch — issue #75).
	Tag string
	// Branch overrides the entry's Ref as the target branch. Defaults
	// to the entry's Ref, then the repo's HEAD branch.
	Branch  string
	Message string
	Author  string
	Email   string
	DryRun  bool
	// AutoCommit, when false (default), causes the publish to refuse a
	// dirty working tree — silent auto-commits surprise users with WIP
	// notes, debug prints, or secrets ending up on the remote (issue #83).
	// When true, the publish stages-and-commits dirty changes as before.
	AutoCommit bool
	// Layout selects the on-fork repo layout for the push: "root" pushes
	// the skill at the repo root (single-skill repo), "nested" pushes
	// under skills/<name>/ (multi-skill registry). Empty defaults to
	// "root" when ForkURL is set (single-skill fork is the common case)
	// and "nested" otherwise. Issue #70.
	Layout string
}

// PublishInstalledResult summarises the publish.
type PublishInstalledResult struct {
	Skill        string `json:"skill"`
	Remote       string `json:"remote"`
	Branch       string `json:"branch"`
	Tag          string `json:"tag,omitempty"`
	Commit       string `json:"commit"`
	DryRun       bool   `json:"dry_run"`
	Migrated     bool   `json:"migrated,omitempty"`
	ForkedFrom   string `json:"forked_from,omitempty"`
	UpstreamPath string `json:"upstream_path,omitempty"` // the dir we pushed from
	Layout       string `json:"layout,omitempty"`        // "root" or "nested" (issue #70)
	// NothingToPublish is true when the eject dir was clean and no new
	// commit was created. cmd/publish.go switches its success message to
	// "Nothing to publish" so pipelines can tell idle reruns from real
	// publishes (issue #84).
	NothingToPublish bool `json:"nothing_to_publish,omitempty"`
}

// PublishInstalled commits + pushes the local edit copy of an installed
// skill to its Source URL (or a --fork override). When --fork is supplied,
// the SKILL.md frontmatter is stamped with `forked-from: <upstream>@<sha>`
// before the commit, and origin is rewritten to the fork URL. When
// --migrate is also set, the lock entry's Source is updated post-push so
// subsequent `qvr publish` calls track the fork.
//
// Both root and nested layouts stage through a temp clone of the upstream
// (issue #86, #95, #98): the user's eject dir is never modified, the
// publish-time commit is one clean cherry-pick on top of the upstream's
// branch (no history graft from the eject dir's synthetic-init repo), and
// the forked-from stamp lands in the stage, never in the eject WD.
//
// The entry is mutated in place on success (Commit advances to the eject
// dir's HEAD — the snapshot the user just published — and Source/
// SourceUpstream may flip for --fork --migrate).
func (p *Publisher) PublishInstalled(ctx context.Context, req PublishInstalledRequest) (*PublishInstalledResult, error) {
	e := req.Entry
	if e == nil {
		return nil, errors.New("publish: nil entry")
	}
	if e.IsLink() {
		return nil, errors.New("cannot publish a link install — edit the source directly and push with raw git")
	}
	if !e.IsEdit() {
		return nil, ErrPublishNoEdit
	}
	if e.EditPath == "" {
		return nil, errors.New("publish: edit-mode entry missing EditPath — run `qvr edit` again to re-eject")
	}

	editAbs := e.EditPath
	if !filepath.IsAbs(editAbs) {
		editAbs = filepath.Join(req.ProjectRoot, e.EditPath)
	}
	if _, err := os.Stat(filepath.Join(editAbs, "SKILL.md")); err != nil {
		return nil, fmt.Errorf("publish %s: edit dir %s has no SKILL.md: %w", e.Name, editAbs, err)
	}

	remoteURL := e.Source
	if req.ForkURL != "" {
		remoteURL = req.ForkURL
	}
	if remoteURL == "" {
		return nil, ErrPublishNoSource
	}

	// Validate the eject dir before any remote is touched — same gate as
	// the path-mode publisher.
	loaded, err := LoadFromPath(editAbs)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSkillPath, err)
	}
	if vr := Validate(loaded); !vr.Valid {
		var msgs []string
		for _, ve := range vr.Errors {
			msgs = append(msgs, ve.Error())
		}
		return nil, fmt.Errorf("skill validation failed:\n  %s", strings.Join(msgs, "\n  "))
	}

	// Compute provenance value once for both dry-run reporting and the
	// stamp-into-stage step below. Built from the eject entry's source
	// info, never read from the stage.
	forkedFromValue := ""
	if req.ForkURL != "" {
		upstream := e.SourceUpstream
		if upstream == "" {
			upstream = e.Source
		}
		short := e.Commit
		if len(short) > 7 {
			short = short[:7]
		}
		if upstream != "" {
			forkedFromValue = fmt.Sprintf("%s@%s", upstream, short)
		}
	}

	layout := req.Layout
	if layout == "" {
		if req.ForkURL != "" {
			layout = "root"
		} else {
			layout = "nested"
		}
	}
	if layout != "root" && layout != "nested" {
		return nil, fmt.Errorf("publish: invalid --layout %q (want root|nested)", layout)
	}

	// Dirty-WD guard (issue #83). Refuse to silently absorb uncommitted
	// edits in the eject dir unless --auto-commit. Runs BEFORE any stage
	// work so the user's WIP isn't picked up mid-publish.
	editStatus, err := readDirtyStatus(editAbs)
	if err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}
	if !editStatus.IsClean() && !req.AutoCommit {
		dirty := dirtyFilesShortList(editStatus)
		return nil, fmt.Errorf("publish %s: refuse to silently auto-commit dirty changes — pass --auto-commit to override, or `git -C %s commit` first (issue #74).\n  Dirty files: %s",
			e.Name, editAbs, dirty)
	}

	if req.DryRun {
		// Dry-run: mirror the real publish's branch precedence so the
		// reported target matches what the next real publish will do. We
		// skip the clone (that's what "dry-run" buys you) but DO call
		// ls-remote --symref — it's a refs-metadata round-trip, no blobs,
		// and without it dry-run prints entry.Ref while the real publish
		// goes to the symref-resolved branch (the user-reported divergence
		// for issue #95). Precedence here matches the real path minus the
		// stage-HEAD step (which requires the clone we're avoiding).
		branch := req.Branch
		if branch == "" {
			if def, dErr := p.Git.RemoteDefaultBranch(ctx, remoteURL); dErr == nil && def != "" {
				branch = def
			}
		}
		if branch == "" && e.Ref != "" {
			branch = e.Ref
		}
		if branch == "" {
			branch = "main"
		}
		return &PublishInstalledResult{
			Skill:        e.Name,
			Remote:       remoteURL,
			Branch:       branch,
			Tag:          req.Tag,
			DryRun:       true,
			ForkedFrom:   forkedFromValue,
			UpstreamPath: editAbs,
			Migrated:     req.ForkURL != "" && req.Migrate,
			Layout:       layout,
		}, nil
	}

	// Stage clone. Both layouts now go through here (issue #86: eject dir
	// is never mutated; issue #95: branch comes from the stage's clone,
	// not the eject's `git init` default of "master"; issue #98: the
	// forked-from stamp lives in the stage, not the user's WD).
	tmp, terr := os.MkdirTemp("", "quiver-publish-installed-*")
	if terr != nil {
		return nil, fmt.Errorf("temp dir: %w", terr)
	}
	defer os.RemoveAll(tmp)
	stageDir := filepath.Join(tmp, "stage")

	stagedRepo, emptyOrNew, cloneErr := openOrInitStage(ctx, p.Git, remoteURL, stageDir)
	if cloneErr != nil {
		return nil, cloneErr
	}
	stagedWt, err := stagedRepo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("stage worktree: %w", err)
	}

	// Branch resolution (issue #95).
	//  1. --branch <X> explicit.
	//  2. Stage clone's HEAD branch (== upstream's default by
	//     construction — `git clone` checks it out). Cheap; already loaded.
	//  3. Remote HEAD symref via `git ls-remote --symref` — authoritative
	//     when the stage's HEAD is missing or detached (which happens on
	//     emptyOrNew, fork-to-new-remote, and any host that doesn't include
	//     a symref header in clone). One extra network round-trip but
	//     correct under upstream renames (master → main).
	//  4. entry.Ref (last-published label) — fallback only; stale by
	//     construction once the upstream renames.
	//  5. "main".
	branch := req.Branch
	if branch == "" && !emptyOrNew {
		if head, hErr := stagedRepo.Head(); hErr == nil && head.Name().IsBranch() {
			branch = head.Name().Short()
		}
	}
	if branch == "" {
		if def, dErr := p.Git.RemoteDefaultBranch(ctx, remoteURL); dErr == nil && def != "" {
			branch = def
		}
	}
	if branch == "" && e.Ref != "" {
		branch = e.Ref
	}
	if branch == "" {
		branch = "main"
	}

	// Position the stage on `branch`. Empty upstreams get HEAD pointed at
	// the future branch so the first commit creates it; populated
	// upstreams get a checkout (auto-creating from current HEAD if the
	// requested branch doesn't exist on the remote yet — same semantics
	// as the path-mode greenfield publish for new branches).
	if emptyOrNew {
		symRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(branch))
		if err := stagedRepo.Storer.SetReference(symRef); err != nil {
			return nil, fmt.Errorf("set HEAD on empty stage: %w", err)
		}
	} else {
		if err := checkoutPublishBranch(stagedRepo, stagedWt, branch, "HEAD"); err != nil {
			return nil, fmt.Errorf("checkout %s in stage: %w", branch, err)
		}
	}

	// Resolve content destination inside the stage.
	contentDest := stageDir
	if layout == "nested" {
		nestedSub := e.Path
		if nestedSub == "" {
			nestedSub = filepath.Join("skills", e.Name)
		}
		contentDest = filepath.Join(stageDir, nestedSub)
	}

	// Wipe the destination so deletions in the eject dir propagate as
	// deletions on the remote. For root layout, preserve `.git/`; for
	// nested, rm -rf the subdir entirely.
	if layout == "root" {
		if err := wipeStageContents(stageDir); err != nil {
			return nil, fmt.Errorf("wipe stage: %w", err)
		}
	} else {
		if err := os.RemoveAll(contentDest); err != nil {
			return nil, fmt.Errorf("clean nested dest: %w", err)
		}
	}

	if err := copyDir(editAbs, contentDest); err != nil {
		return nil, fmt.Errorf("copy skill into stage: %w", err)
	}

	// Stamp forked-from in the stage (never in eject dir — issue #98).
	if forkedFromValue != "" {
		if err := stampForkedFrom(filepath.Join(contentDest, "SKILL.md"), forkedFromValue); err != nil {
			return nil, fmt.Errorf("stamp forked-from: %w", err)
		}
	}

	// Stage + commit.
	if err := stagedWt.AddWithOptions(&gogit.AddOptions{All: true}); err != nil {
		return nil, fmt.Errorf("stage: %w", err)
	}
	stageStatus, err := stagedWt.Status()
	if err != nil {
		return nil, fmt.Errorf("stage status: %w", err)
	}

	author := req.Author
	if author == "" {
		author = "quiver"
	}
	email := req.Email
	if email == "" {
		email = "quiver@localhost"
	}
	message := req.Message
	if message == "" {
		message = fmt.Sprintf("qvr: publish %s", e.Name)
		if req.Tag != "" {
			message = fmt.Sprintf("qvr: release %s %s", e.Name, req.Tag)
		}
	}

	commitHash := plumbing.ZeroHash
	switch {
	case !stageStatus.IsClean():
		h, cerr := stagedWt.Commit(message, &gogit.CommitOptions{
			Author: &object.Signature{Name: author, Email: email, When: time.Now()},
		})
		if cerr != nil {
			return nil, fmt.Errorf("commit: %w", cerr)
		}
		commitHash = h
	case emptyOrNew:
		// Empty upstream + identical content to a freshly-initialised
		// stage shouldn't reach a clean status, but defensively allow
		// an empty initial commit so the branch exists on push.
		h, cerr := stagedWt.Commit(message, &gogit.CommitOptions{
			Author:            &object.Signature{Name: author, Email: email, When: time.Now()},
			AllowEmptyCommits: true,
		})
		if cerr != nil {
			return nil, fmt.Errorf("commit (empty init): %w", cerr)
		}
		commitHash = h
	default:
		if head, hErr := stagedRepo.Head(); hErr == nil {
			commitHash = head.Hash()
		}
	}

	// Nothing-to-publish: stage's HEAD already matches remote branch.
	// Always re-check via ls-remote because the stage was cloned moments
	// ago but a concurrent push could have advanced things. Tag publishes
	// proceed past this check — the tag is the unit of work.
	nothingToPublish := false
	if req.Tag == "" {
		remoteRefs, lerr := p.Git.LsRemote(ctx, remoteURL)
		if lerr == nil && remoteRefs != nil {
			if remoteHead, ok := remoteRefs.Refs["refs/heads/"+branch]; ok && remoteHead == commitHash.String() {
				nothingToPublish = true
			}
		}
	}
	if nothingToPublish {
		return &PublishInstalledResult{
			Skill:            e.Name,
			Remote:           remoteURL,
			Branch:           branch,
			Commit:           commitHash.String(),
			ForkedFrom:       forkedFromValue,
			UpstreamPath:     stageDir,
			Layout:           layout,
			NothingToPublish: true,
		}, nil
	}

	// Push. Atomic for tag publishes (issue #75 — branch and tag either
	// both land or neither). For branch-only, single-refspec push goes
	// through the older non-atomic protocol for compatibility.
	refSpecs := []string{fmt.Sprintf("refs/heads/%s:refs/heads/%s", branch, branch)}
	var tagCreated bool
	if req.Tag != "" {
		tagRef := plumbing.NewTagReferenceName(req.Tag)
		if _, err := stagedRepo.Reference(tagRef, false); err == nil {
			return nil, fmt.Errorf("tag %s already exists on remote", req.Tag)
		}
		if _, err := stagedRepo.CreateTag(req.Tag, commitHash, &gogit.CreateTagOptions{
			Tagger:  &object.Signature{Name: author, Email: email, When: time.Now()},
			Message: fmt.Sprintf("Release %s", req.Tag),
		}); err != nil {
			return nil, fmt.Errorf("create tag: %w", err)
		}
		tagCreated = true
		refSpecs = append(refSpecs, fmt.Sprintf("refs/tags/%s:refs/tags/%s", req.Tag, req.Tag))
	}
	if err := p.Git.Push(ctx, stageDir, "origin", refSpecs); err != nil {
		if tagCreated {
			_ = stagedRepo.DeleteTag(req.Tag)
		}
		return nil, fmt.Errorf("push: %w", err)
	}

	// Update entry on success. e.Commit tracks the eject dir's HEAD —
	// the user-committed snapshot that we just published — NOT the
	// stage's publish commit (which is a clean cherry-pick on top of the
	// upstream and has no relation to the eject dir's git history). This
	// keeps `qvr doctor` / `qvr lock verify` integrity checks honest:
	// they compare against the eject dir's repo, which doesn't know
	// about the stage's commit.
	if head, hErr := readRepoHead(editAbs); hErr == nil && head != "" {
		e.Commit = head
	}
	if req.ForkURL != "" && req.Migrate {
		if e.SourceUpstream == "" {
			e.SourceUpstream = e.Source
		}
		e.Source = req.ForkURL
		e.Registry = ""
	}
	if h, hErr := canonical.HashSubtreeFromDisk(editAbs); hErr == nil {
		e.SubtreeHash = h
	}

	return &PublishInstalledResult{
		Skill:            e.Name,
		Remote:           remoteURL,
		Branch:           branch,
		Tag:              req.Tag,
		Commit:           commitHash.String(),
		ForkedFrom:       forkedFromValue,
		UpstreamPath:     stageDir,
		Migrated:         req.ForkURL != "" && req.Migrate,
		Layout:           layout,
		NothingToPublish: false,
	}, nil
}

// openOrInitStage clones remoteURL into stageDir. If the remote returns
// "not found" (the fork URL points at an empty/new repo on the host), it
// falls back to `git init` + `origin = remoteURL` so the publish can
// still seed the first commit. Returns (repo, emptyOrNew, err) where
// emptyOrNew is true only on the init path.
func openOrInitStage(ctx context.Context, gc git.GitClient, remoteURL, stageDir string) (*gogit.Repository, bool, error) {
	err := gc.Clone(ctx, remoteURL, stageDir)
	if err == nil {
		repo, oerr := gogit.PlainOpen(stageDir)
		if oerr != nil {
			return nil, false, fmt.Errorf("open stage: %w", oerr)
		}
		// A `git clone` of an empty repo leaves the local clone with no
		// HEAD branch. Detect that and switch into the empty-stage flow
		// (HEAD set to a symbolic ref on the requested branch, first
		// commit creates it).
		if _, headErr := repo.Head(); headErr != nil {
			return repo, true, nil
		}
		return repo, false, nil
	}
	if !errors.Is(err, git.ErrRepoNotFound) {
		return nil, false, fmt.Errorf("clone remote: %w", err)
	}
	// Remote refused with "not found" — treat as a freshly-created empty
	// repo on the host (the common case for `qvr publish --fork <new-url>`).
	if mkErr := os.MkdirAll(stageDir, 0o755); mkErr != nil {
		return nil, false, fmt.Errorf("create stage dir: %w", mkErr)
	}
	repo, ierr := gogit.PlainInit(stageDir, false)
	if ierr != nil {
		return nil, false, fmt.Errorf("init stage: %w", ierr)
	}
	if oerr := setRepoOriginURL(repo, remoteURL); oerr != nil {
		return nil, false, fmt.Errorf("set stage origin: %w", oerr)
	}
	return repo, true, nil
}

// wipeStageContents removes every top-level entry from dir except `.git/`,
// used before copying the eject tree into a root-layout stage so deletions
// in the eject dir propagate as removals on the upstream.
func wipeStageContents(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read stage: %w", err)
	}
	for _, ent := range entries {
		if ent.Name() == ".git" {
			continue
		}
		if rerr := os.RemoveAll(filepath.Join(dir, ent.Name())); rerr != nil {
			return fmt.Errorf("remove %s: %w", ent.Name(), rerr)
		}
	}
	return nil
}

// readDirtyStatus opens the repo at dir and returns its uncommitted-changes
// status. Used by PublishInstalled's auto-commit guard (issue #83) to decide
// whether to refuse the publish or proceed.
func readDirtyStatus(dir string) (gogit.Status, error) {
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("worktree: %w", err)
	}
	return wt.Status()
}

// dirtyFilesShortList renders up to 5 dirty paths into a comma-joined string
// suitable for an error message. Truncates with `(+N more)` past the limit so
// terminals don't get flooded for repos with many uncommitted changes.
func dirtyFilesShortList(status gogit.Status) string {
	var names []string
	for path := range status {
		names = append(names, path)
	}
	sort.Strings(names)
	const limit = 5
	if len(names) <= limit {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s (+%d more)", strings.Join(names[:limit], ", "), len(names)-limit)
}

// setRepoOriginURL is a local helper mirroring git.setOriginURL (which
// is package-private to internal/git/worktree.go). Replaces the existing
// origin URL or creates the remote when absent.
func setRepoOriginURL(repo *gogit.Repository, url string) error {
	cfg, err := repo.Config()
	if err != nil {
		return err
	}
	if cfg.Remotes == nil {
		cfg.Remotes = map[string]*gogitcfg.RemoteConfig{}
	}
	if r, ok := cfg.Remotes["origin"]; ok {
		r.URLs = []string{url}
	} else {
		cfg.Remotes["origin"] = &gogitcfg.RemoteConfig{Name: "origin", URLs: []string{url}}
	}
	return repo.SetConfig(cfg)
}

// stampForkedFrom rewrites a SKILL.md to insert (or replace) the
// `forked-from: <value>` key in its YAML frontmatter. Preserves the rest
// of the frontmatter and body verbatim — line-based edit, no YAML
// round-tripping (which would reorder keys and lose user comments).
func stampForkedFrom(skillPath, value string) error {
	data, err := os.ReadFile(skillPath)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "---" {
		return fmt.Errorf("%s: missing frontmatter delimiter", skillPath)
	}
	// Find the closing "---" line.
	closeIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			closeIdx = i
			break
		}
	}
	if closeIdx == -1 {
		return fmt.Errorf("%s: missing closing frontmatter delimiter", skillPath)
	}
	// Look for an existing `forked-from:` line and replace, else insert
	// just before the closing "---".
	replaced := false
	for i := 1; i < closeIdx; i++ {
		if strings.HasPrefix(strings.TrimLeft(lines[i], " \t"), "forked-from:") {
			lines[i] = fmt.Sprintf("forked-from: %s", value)
			replaced = true
			break
		}
	}
	if !replaced {
		insert := fmt.Sprintf("forked-from: %s", value)
		out := make([]string, 0, len(lines)+1)
		out = append(out, lines[:closeIdx]...)
		out = append(out, insert)
		out = append(out, lines[closeIdx:]...)
		lines = out
	}
	return os.WriteFile(skillPath, []byte(strings.Join(lines, "\n")), 0o644)
}

// Compile-time guard: keep git.GitClient referenced so go-mod-tidy doesn't
// strip the import when this file is the only one using its push contract
// transitively. The interface itself lives on Publisher.Git already; this
// is belt-and-braces against future refactors.
var _ git.GitClient = (*git.GoGitClient)(nil)
