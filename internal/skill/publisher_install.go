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
// The entry is mutated in place on success (Commit advances, Source and
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
		// Without an edit dir there's no local git state to push from.
		// We could in principle push a `qvr edit` automatically, but
		// that's a surprising mutation; require explicit edit.
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

	// Determine remote URL.
	remoteURL := e.Source
	if req.ForkURL != "" {
		remoteURL = req.ForkURL
	}
	if remoteURL == "" {
		return nil, ErrPublishNoSource
	}

	// Determine target branch. For edit-mode entries the local git repo's
	// HEAD branch is authoritative (set by `qvr edit`'s initial commit and
	// whatever branch the user has checked out since). entry.Ref records
	// the original upstream ref label, not the local branch, so it's only
	// a fallback when HEAD lookup fails. `--branch` overrides everything.
	branch := req.Branch

	// Validate the staged skill before touching the upstream — same
	// philosophy as `qvr publish` path mode: scan/validation must pass
	// before any remote is contacted.
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

	// For --fork, compute provenance value now (so dry-run can report it),
	// but DEFER the actual SKILL.md write until after the dirty-WD guard
	// below. Otherwise qvr's own stamp dirties the working tree and trips
	// the guard that's meant to catch *user* WIP — chicken/egg with #83
	// (issue #98).
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

	// Open the edit-dir repo. `qvr edit` initialised it with one commit;
	// subsequent edits to files in editAbs are uncommitted dirt we'll stage.
	repo, err := gogit.PlainOpen(editAbs)
	if err != nil {
		return nil, fmt.Errorf("open edit repo: %w", err)
	}

	// Resolve target branch. Precedence (issue #95):
	//  1. --branch <X>   (explicit)
	//  2. Remote's default branch (ls-remote HEAD → matching refs/heads/*)
	//  3. Local eject HEAD's branch name
	//  4. entry.Ref
	//  5. "main"
	// The remote-default lookup matters because the local eject repo is
	// initialised by `git init` and always sits on "master" — without it,
	// we'd default to "master" even when the upstream uses "main".
	// Skip the network round-trip in dry-run mode: we don't want
	// `--dry-run` to hang on a fake URL.
	if branch == "" && !req.DryRun {
		if remote, lerr := p.Git.LsRemote(ctx, remoteURL); lerr == nil && remote != nil {
			if headHash, ok := remote.Refs["HEAD"]; ok {
				for name, hash := range remote.Refs {
					if hash == headHash && strings.HasPrefix(name, "refs/heads/") {
						branch = strings.TrimPrefix(name, "refs/heads/")
						break
					}
				}
			}
		}
	}
	if branch == "" {
		if head, hErr := repo.Head(); hErr == nil && head.Name().IsBranch() {
			branch = head.Name().Short()
		}
	}
	if branch == "" && e.Ref != "" {
		branch = e.Ref
	}
	if branch == "" {
		branch = "main"
	}

	// Resolve effective layout (issue #70). Default: "root" for --fork
	// (single-skill repo case), "nested" otherwise (push back to a
	// multi-skill registry). Explicit req.Layout overrides.
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

	if req.DryRun {
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

	// Dirty-WD guard (issue #83): refuse a silent auto-commit unless the
	// caller passed AutoCommit=true. Default behavior used to stage and
	// commit any uncommitted edits, which surprised users whose WIP debug
	// notes / secrets ended up on the remote.
	statusBeforeStage, err := readDirtyStatus(editAbs)
	if err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}
	if !statusBeforeStage.IsClean() && !req.AutoCommit {
		dirty := dirtyFilesShortList(statusBeforeStage)
		return nil, fmt.Errorf("publish %s: refuse to silently auto-commit dirty changes — pass --auto-commit to override, or `git -C %s commit` first.\n  Dirty files: %s",
			e.Name, editAbs, dirty)
	}

	// Dirty check passed — NOW it's safe to stamp the forked-from line.
	// The stamp dirties the WD; the subsequent stage+commit below absorbs
	// it into the publish commit (issue #98).
	if forkedFromValue != "" {
		if err := stampForkedFrom(filepath.Join(editAbs, "SKILL.md"), forkedFromValue); err != nil {
			return nil, fmt.Errorf("stamp forked-from: %w", err)
		}
	}

	// Push-flow: pick the right repo to commit-and-push from. Root layout
	// pushes from the edit dir itself (single-skill repo). Nested layout
	// clones the upstream into a temp dir, copies the edit dir contents
	// into <stage>/<entry.Path>/, and pushes from there.
	pushFromAbs := editAbs
	pushRepo := repo
	cleanupTmp := func() {}
	if layout == "nested" {
		nestedDir := e.Path
		if nestedDir == "" {
			nestedDir = filepath.Join("skills", e.Name)
		}
		tmp, terr := os.MkdirTemp("", "quiver-publish-installed-*")
		if terr != nil {
			return nil, fmt.Errorf("temp dir: %w", terr)
		}
		cleanupTmp = func() { _ = os.RemoveAll(tmp) }
		stageDir := filepath.Join(tmp, "stage")
		if err := p.Git.Clone(ctx, remoteURL, stageDir); err != nil {
			cleanupTmp()
			return nil, fmt.Errorf("clone remote for nested publish: %w", err)
		}
		stagedRepo, err := gogit.PlainOpen(stageDir)
		if err != nil {
			cleanupTmp()
			return nil, fmt.Errorf("open stage repo: %w", err)
		}
		stagedWt, err := stagedRepo.Worktree()
		if err != nil {
			cleanupTmp()
			return nil, fmt.Errorf("stage worktree: %w", err)
		}
		// Best-effort: switch to the target branch in the stage. New
		// branches aren't auto-created here — installed-mode publish is
		// "update the existing skill", not "branch from a default".
		if err := checkoutPublishBranch(stagedRepo, stagedWt, branch, ""); err != nil {
			cleanupTmp()
			return nil, fmt.Errorf("checkout %s in stage: %w", branch, err)
		}
		dest := filepath.Join(stageDir, nestedDir)
		// Wipe any existing copy so deletions in the edit dir flow through
		// to the registry side (a removed file in the edit copy should
		// land as a deletion on the remote).
		if err := os.RemoveAll(dest); err != nil {
			cleanupTmp()
			return nil, fmt.Errorf("clean nested dest: %w", err)
		}
		if err := copyDir(editAbs, dest); err != nil {
			cleanupTmp()
			return nil, fmt.Errorf("copy skill into nested layout: %w", err)
		}
		pushFromAbs = stageDir
		pushRepo = stagedRepo
	}
	defer cleanupTmp()

	wt, err := pushRepo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("worktree handle: %w", err)
	}
	if err := wt.AddWithOptions(&gogit.AddOptions{All: true}); err != nil {
		return nil, fmt.Errorf("stage: %w", err)
	}
	status, err := wt.Status()
	if err != nil {
		return nil, fmt.Errorf("status: %w", err)
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

	// Capture pre-commit HEAD so a push failure can rewind the local commit
	// (issue #86). Empty when the repo has no prior HEAD (impossible after
	// `qvr edit` and after a registry clone, but defensive).
	var preCommitHead plumbing.Hash
	if head, hErr := pushRepo.Head(); hErr == nil {
		preCommitHead = head.Hash()
	}

	commitHash := plumbing.ZeroHash
	if !status.IsClean() {
		h, cerr := wt.Commit(message, &gogit.CommitOptions{
			Author: &object.Signature{Name: author, Email: email, When: time.Now()},
		})
		if cerr != nil {
			return nil, fmt.Errorf("commit: %w", cerr)
		}
		commitHash = h
	} else if head, hErr := pushRepo.Head(); hErr == nil {
		commitHash = head.Hash()
	}

	// For root layout, set origin on the edit repo so future raw-git work
	// on the edit dir tracks the right remote. Nested layout uses a temp
	// clone whose origin is already set by Clone.
	if layout == "root" {
		if err := setRepoOriginURL(pushRepo, remoteURL); err != nil {
			return nil, fmt.Errorf("set origin: %w", err)
		}
	}

	// Decide nothing-to-publish by asking the REMOTE, not by inspecting the
	// local WD. WD-cleanliness only tells us "the user hasn't made uncommitted
	// edits since the last commit"; it says nothing about whether the
	// committed history has been pushed. For a brand-new --fork remote that
	// doesn't have the branch at all, we MUST push (issue #97).
	nothingToPublish := false
	if req.Tag == "" {
		remoteRefs, lerr := p.Git.LsRemote(ctx, remoteURL)
		if lerr == nil && remoteRefs != nil {
			remoteHead, ok := remoteRefs.Refs["refs/heads/"+branch]
			if ok && remoteHead == commitHash.String() {
				nothingToPublish = true
			}
		}
		// LsRemote errors are non-fatal here — we just don't short-circuit,
		// and the regular push path handles the upstream reality.
	}

	if nothingToPublish {
		// True no-op publish: bail before touching the remote. Tag-cutting
		// reruns still proceed below so `qvr publish --tag vN` is valid on
		// a clean tree (the tag is the unit of work).
		return &PublishInstalledResult{
			Skill:            e.Name,
			Remote:           remoteURL,
			Branch:           branch,
			Commit:           commitHash.String(),
			ForkedFrom:       forkedFromValue,
			UpstreamPath:     pushFromAbs,
			Layout:           layout,
			NothingToPublish: true,
		}, nil
	}

	// Push branch first; if it succeeds, push the tag in a separate call.
	// This avoids the orphan-tag-on-remote case (issue #75) where today's
	// non-atomic combined push could leave the tag on origin pointing at
	// a commit not reachable from the released branch.
	//
	// For root layout we use HEAD as the source side of the refspec so a
	// local branch name mismatch (eject dir is on "master", target is
	// "main") doesn't blow up the push (issue #95). For nested layout the
	// staged clone was just put on the target branch by checkoutPublishBranch,
	// so the local branch name and the target match.
	srcRef := "HEAD"
	if layout == "nested" {
		srcRef = "refs/heads/" + branch
	}
	branchSpec := []string{fmt.Sprintf("%s:refs/heads/%s", srcRef, branch)}
	if err := p.Git.Push(ctx, pushFromAbs, "origin", branchSpec); err != nil {
		// Roll back the local commit so a retry sees the same starting
		// state — without this, a failed push leaves a phantom commit on
		// top of the edit dir's history that the next successful publish
		// would ship (issue #86). Best-effort: failure to rewind is logged
		// upstream, not a separate error.
		if commitHash != plumbing.ZeroHash && preCommitHead != plumbing.ZeroHash {
			_ = rewindToHead(pushRepo, preCommitHead)
		}
		return nil, fmt.Errorf("push branch: %w", err)
	}

	if req.Tag != "" {
		tagRef := plumbing.NewTagReferenceName(req.Tag)
		if _, err := pushRepo.Reference(tagRef, false); err == nil {
			return nil, fmt.Errorf("tag %s already exists locally", req.Tag)
		}
		if _, err := pushRepo.CreateTag(req.Tag, commitHash, &gogit.CreateTagOptions{
			Tagger:  &object.Signature{Name: author, Email: email, When: time.Now()},
			Message: fmt.Sprintf("Release %s", req.Tag),
		}); err != nil {
			return nil, fmt.Errorf("create tag: %w", err)
		}
		tagSpec := []string{fmt.Sprintf("refs/tags/%s:refs/tags/%s", req.Tag, req.Tag)}
		if err := p.Git.Push(ctx, pushFromAbs, "origin", tagSpec); err != nil {
			// Tag push failed but branch already landed. Roll back the
			// local tag (the remote one was never created) so the retry
			// flow is clean. Branch is intentionally left advanced — we
			// achieved the publish, just not the release tag.
			_ = pushRepo.DeleteTag(req.Tag)
			return nil, fmt.Errorf("push tag: %w", err)
		}
	}

	// Update the entry on success.
	e.Commit = commitHash.String()
	if req.ForkURL != "" && req.Migrate {
		// SourceUpstream may already be set (the original upstream from
		// the eject step). Preserve it; only fill it on first migrate.
		if e.SourceUpstream == "" {
			e.SourceUpstream = e.Source
		}
		e.Source = req.ForkURL
		// Clear Registry so future qvr add/info doesn't think the fork
		// belongs to the now-stale registry name. Issue #85.
		e.Registry = ""
	}
	// Refresh subtree hash against the post-publish dir.
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
		UpstreamPath:     pushFromAbs,
		Migrated:         req.ForkURL != "" && req.Migrate,
		Layout:           layout,
		NothingToPublish: nothingToPublish,
	}, nil
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

// rewindToHead force-resets the working copy to the given commit. Used to
// roll back a local commit when the subsequent push fails (issue #86) so a
// retry sees the same starting state. Best-effort: errors are returned but
// callers treat the rewind as advisory — the user can still recover with
// raw git.
func rewindToHead(repo *gogit.Repository, target plumbing.Hash) error {
	headRef := plumbing.NewHashReference(plumbing.HEAD, target)
	if err := repo.Storer.SetReference(headRef); err != nil {
		return fmt.Errorf("set HEAD: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("worktree: %w", err)
	}
	return wt.Reset(&gogit.ResetOptions{Mode: gogit.MixedReset, Commit: target})
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
