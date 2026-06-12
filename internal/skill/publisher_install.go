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

	"github.com/astra-sh/qvr/internal/canonical"
	"github.com/astra-sh/qvr/internal/git"
	"github.com/astra-sh/qvr/internal/model"
)

var (
	// ErrPublishNoEdit is returned when publishing an installed skill that
	// has no edit dir: only an ejected (`qvr edit`) skill carries the local
	// git repo the publish works off.
	ErrPublishNoEdit = errors.New("skill must be ejected with `qvr edit` before publishing")
	// ErrPublishNoSource is returned when the lock entry records no Source
	// URL, so there is no remote to push to without --registry or --fork.
	ErrPublishNoSource = errors.New("skill entry has no Source URL — publish it into a registry with `qvr publish <name> --registry <org>/<repo>`, or to a new remote with --fork <url>")
	// ErrPublishForkRequired is returned when --fork is passed for a skill
	// that is not ejected — the fork push needs the edit dir's repo.
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
	// ForkURL re-targets the publish to a new remote: origin is rewritten
	// to ForkURL and the push lands there. SKILL.md is never mutated — the
	// published artifact is byte-identical to the eject dir's checked-in
	// SKILL.md. Migrate=true rewrites entry.Source so future publishes
	// track the fork, records the upstream in entry.ForkedFrom, and clears
	// entry.Registry (issue #85) because the fork is registry-independent.
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
// skill to its Source URL (or a --fork override). SKILL.md is never
// mutated by this path — fork provenance lives in the lockfile entry
// (ForkedFrom), not the artifact. When --fork is supplied, origin is
// rewritten to the fork URL. When --migrate is also set, the lock
// entry's Source is updated post-push so subsequent `qvr publish` calls
// track the fork, and entry.ForkedFrom records the upstream the fork
// derived from.
//
// Both root and nested layouts stage through a temp clone of the upstream
// (issue #86, #95): the user's eject dir is never modified, and the
// publish-time commit is one clean cherry-pick on top of the upstream's
// branch (no history graft from the eject dir's synthetic-init repo).
//
// The entry is mutated in place on success (Commit advances to the eject
// dir's HEAD — the snapshot the user just published — and Source/
// SourceUpstream/ForkedFrom may flip for --fork --migrate).
func (p *Publisher) PublishInstalled(ctx context.Context, req PublishInstalledRequest) (*PublishInstalledResult, error) {
	e := req.Entry
	editAbs, remoteURL, err := prepublishResolve(req)
	if err != nil {
		return nil, err
	}

	if err := lintEjectDir(editAbs, e); err != nil {
		return nil, err
	}

	// Compute provenance value for both dry-run reporting and the
	// lockfile ForkedFrom field set on --migrate below. Built from the
	// eject entry's source info; never written to any SKILL.md.
	forkedFromValue := computeForkedFrom(req, e)

	layout, err := resolvePublishLayout(req, e)
	if err != nil {
		return nil, err
	}
	// The version tag to create/report: namespaced per skill for a nested
	// (multi-skill) registry, bare for a root (single-skill / --fork) repo so
	// two skills can debut at the same semver without colliding (issue #152).
	tagName := req.Tag
	if layout == "nested" {
		tagName = skillVersionTag(e.Name, req.Tag)
	}

	// A mode:edit dir scaffolded by `qvr create` (or one created by an older
	// binary that didn't init git) may have no .git/ yet. Publish needs a repo
	// to read dirty-status and stamp the source commit, so initialize it in
	// place instead of aborting with the opaque "open: repository does not
	// exist" (issue #150).
	if err := ensureEditRepo(editAbs, e.Name); err != nil {
		return nil, err
	}

	// Dirty-WD guard (issue #83). Refuse to silently absorb uncommitted
	// edits in the eject dir unless --auto-commit. Runs BEFORE any stage
	// work so the user's WIP isn't picked up mid-publish.
	if err := guardDirtyEditDir(editAbs, e, req.AutoCommit); err != nil {
		return nil, err
	}

	if req.DryRun {
		return p.publishInstalledDryRun(ctx, req, e, remoteURL, tagName, layout, editAbs, forkedFromValue), nil
	}

	// Stage clone. Both layouts now go through here (issue #86: eject dir
	// is never mutated; issue #95: branch comes from the stage's clone,
	// not the eject's `git init` default of "master"). The SKILL.md that
	// lands in the stage is the eject dir's verbatim — qvr never stamps
	// metadata into the published artifact.
	tmp, terr := os.MkdirTemp("", "quiver-publish-installed-*")
	if terr != nil {
		return nil, fmt.Errorf("temp dir: %w", terr)
	}
	defer os.RemoveAll(tmp)
	stageDir := filepath.Join(tmp, "stage")

	stagedRepo, stagedWt, branch, emptyOrNew, err := p.setUpPublishStage(ctx, req, e, remoteURL, stageDir)
	if err != nil {
		return nil, err
	}

	stageStatus, err := populatePublishStage(stagedWt, e, layout, stageDir, editAbs)
	if err != nil {
		return nil, err
	}

	author, email, message := publishCommitIdentity(req, e)

	commitHash, err := commitPublishStage(stagedRepo, stagedWt, stageStatus, emptyOrNew, author, email, message)
	if err != nil {
		return nil, err
	}

	// Nothing-to-publish: stage's HEAD already matches remote branch.
	// Always re-check via ls-remote because the stage was cloned moments
	// ago but a concurrent push could have advanced things. Tag publishes
	// proceed past this check — the tag is the unit of work.
	if p.publishIsNoop(ctx, req, remoteURL, branch, commitHash) {
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

	if err := p.pushPublishStage(ctx, stagedRepo, stageDir, branch, tagName, commitHash, author, email); err != nil {
		return nil, err
	}

	// Update entry on success. e.Commit tracks the eject dir's HEAD —
	// the user-committed snapshot that we just published — NOT the
	// stage's publish commit (which is a clean cherry-pick on top of the
	// upstream and has no relation to the eject dir's git history). This
	// keeps `qvr doctor` / `qvr lock verify` integrity checks honest:
	// they compare against the eject dir's repo, which doesn't know
	// about the stage's commit.
	finalizePublishedEntry(e, req, editAbs, forkedFromValue)

	return &PublishInstalledResult{
		Skill:            e.Name,
		Remote:           remoteURL,
		Branch:           branch,
		Tag:              tagName,
		Commit:           commitHash.String(),
		ForkedFrom:       forkedFromValue,
		UpstreamPath:     stageDir,
		Migrated:         req.ForkURL != "" && req.Migrate,
		Layout:           layout,
		NothingToPublish: false,
	}, nil
}

// prepublishResolve validates the lock entry, resolves the absolute eject dir
// (with its SKILL.md presence check), and resolves the push remote (entry.Source
// or the --fork override). Returns the eject abs path and remote URL.
func prepublishResolve(req PublishInstalledRequest) (editAbs, remoteURL string, err error) {
	e := req.Entry
	if e == nil {
		return "", "", errors.New("publish: nil entry")
	}
	if e.IsLink() {
		return "", "", errors.New("cannot publish a link install — edit the source directly and push with raw git")
	}
	if !e.IsEdit() {
		return "", "", ErrPublishNoEdit
	}
	if e.EditPath == "" {
		return "", "", errors.New("publish: edit-mode entry missing EditPath — run `qvr edit` again to re-eject")
	}

	editAbs = e.EditPath
	if !filepath.IsAbs(editAbs) {
		editAbs = filepath.Join(req.ProjectRoot, e.EditPath)
	}
	if _, serr := os.Stat(filepath.Join(editAbs, "SKILL.md")); serr != nil {
		return "", "", fmt.Errorf("publish %s: edit dir %s has no SKILL.md: %w", e.Name, editAbs, serr)
	}

	remoteURL = e.Source
	if req.ForkURL != "" {
		remoteURL = req.ForkURL
	}
	if remoteURL == "" {
		return "", "", ErrPublishNoSource
	}
	return editAbs, remoteURL, nil
}

// setUpPublishStage clones (or inits) the stage, opens its worktree, resolves
// the target branch, and positions the stage on it: empty upstreams get HEAD
// pointed at the future branch so the first commit creates it; populated
// upstreams get a checkout (auto-creating from current HEAD if the requested
// branch doesn't exist on the remote yet — same semantics as the path-mode
// greenfield publish for new branches).
func (p *Publisher) setUpPublishStage(ctx context.Context, req PublishInstalledRequest, e *model.LockEntry, remoteURL, stageDir string) (*gogit.Repository, *gogit.Worktree, string, bool, error) {
	stagedRepo, emptyOrNew, cloneErr := openOrInitStage(ctx, p.Git, remoteURL, stageDir)
	if cloneErr != nil {
		return nil, nil, "", false, cloneErr
	}
	stagedWt, err := stagedRepo.Worktree()
	if err != nil {
		return nil, nil, "", false, fmt.Errorf("stage worktree: %w", err)
	}

	branch := p.resolvePublishBranch(ctx, req, e, remoteURL, stagedRepo, emptyOrNew)

	if emptyOrNew {
		symRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(branch))
		if err := stagedRepo.Storer.SetReference(symRef); err != nil {
			return nil, nil, "", false, fmt.Errorf("set HEAD on empty stage: %w", err)
		}
	} else {
		if err := checkoutPublishBranch(stagedRepo, stagedWt, branch, "HEAD"); err != nil {
			return nil, nil, "", false, fmt.Errorf("checkout %s in stage: %w", branch, err)
		}
	}
	return stagedRepo, stagedWt, branch, emptyOrNew, nil
}

// publishIsNoop reports whether the stage's HEAD already matches the remote
// branch (so there's nothing new to push). Tag publishes never short-circuit —
// the tag is the unit of work.
func (p *Publisher) publishIsNoop(ctx context.Context, req PublishInstalledRequest, remoteURL, branch string, commitHash plumbing.Hash) bool {
	if req.Tag != "" {
		return false
	}
	remoteRefs, lerr := p.Git.LsRemote(ctx, remoteURL)
	if lerr == nil && remoteRefs != nil {
		if remoteHead, ok := remoteRefs.Refs["refs/heads/"+branch]; ok && remoteHead == commitHash.String() {
			return true
		}
	}
	return false
}

// lintEjectDir lints the eject dir before any remote is touched — the same
// gate as the path-mode publisher. Aliased installs have SKILL.md `name:` ==
// canonical, but LoadFromPath reads the dir basename as the linter's expected
// name. The dir is the alias, so the name-must-match-dir check would always
// fail; the published artifact carries the canonical name (the registry-side
// identity, not the local alias), so swap the linter's expected name to the
// canonical for aliased entries (issue #104).
func lintEjectDir(editAbs string, e *model.LockEntry) error {
	loaded, err := LoadFromPath(editAbs)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSkillPath, err)
	}
	if e.Canonical != "" {
		loaded.Name = e.Canonical
	}
	if lr := Lint(loaded); !lr.Valid {
		var msgs []string
		for _, ve := range lr.Errors {
			msgs = append(msgs, ve.Error())
		}
		return fmt.Errorf("skill lint failed:\n  %s", strings.Join(msgs, "\n  "))
	}
	return nil
}

// computeForkedFrom builds the lockfile ForkedFrom provenance value ("<upstream>@<sha>")
// for a --fork publish. Returns "" when not forking or when no upstream is known.
func computeForkedFrom(req PublishInstalledRequest, e *model.LockEntry) string {
	if req.ForkURL == "" {
		return ""
	}
	upstream := e.UpstreamSource()
	short := e.Commit
	if len(short) > 7 {
		short = short[:7]
	}
	if upstream != "" {
		return fmt.Sprintf("%s@%s", upstream, short)
	}
	return ""
}

// resolvePublishLayout resolves the on-fork repo layout ("root" | "nested"),
// defaulting from the request and validating an explicit value.
func resolvePublishLayout(req PublishInstalledRequest, e *model.LockEntry) (string, error) {
	layout := req.Layout
	if layout == "" {
		switch {
		case req.ForkURL != "":
			// A fresh --fork target is a single-skill repo: push at root.
			layout = "root"
		case e.Path == ".":
			// The entry already tracks a root-layout (single-skill) source —
			// e.g. a fork migrated by an earlier `--fork --migrate`, whose
			// lock Path is "." (an empty Path still defaults to nested for a
			// multi-skill registry). Re-publishing must STAY at root.
			// Defaulting to nested here computed contentDest == stageDir, and
			// the nested cleanup `os.RemoveAll(contentDest)` wiped the stage
			// clone's .git/, bricking the second release on a migrated fork
			// (#155).
			layout = "root"
		default:
			layout = "nested"
		}
	}
	if layout != "root" && layout != "nested" {
		return "", fmt.Errorf("publish: invalid --layout %q (want root|nested)", layout)
	}
	return layout, nil
}

// guardDirtyEditDir enforces the dirty-WD guard (issue #83): refuse to silently
// absorb uncommitted edits in the eject dir unless --auto-commit is set.
func guardDirtyEditDir(editAbs string, e *model.LockEntry, autoCommit bool) error {
	editStatus, err := readDirtyStatus(editAbs)
	if err != nil {
		return fmt.Errorf("status: %w", err)
	}
	if !editStatus.IsClean() && !autoCommit {
		dirty := dirtyFilesShortList(editStatus)
		return fmt.Errorf("publish %s: refuse to silently auto-commit dirty changes — pass --auto-commit to override, or `git -C %s commit` first (issue #74).\n  Dirty files: %s",
			e.Name, editAbs, dirty)
	}
	return nil
}

// populatePublishStage resolves the content destination inside the stage, wipes
// it (preserving .git/ for root, rm -rf the subdir for nested) so eject-dir
// deletions propagate, copies the eject tree in, stages all changes, and returns
// the resulting worktree status.
func populatePublishStage(stagedWt *gogit.Worktree, e *model.LockEntry, layout, stageDir, editAbs string) (gogit.Status, error) {
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
		// Guard: a nested subdir that resolves to the stage root (e.g. an
		// entry whose Path is "." forced through --layout nested) would make
		// the RemoveAll below delete the clone's .git/ and brick the publish
		// (#155). That's a contradictory request — a root-layout entry must
		// publish with --layout root. Refuse instead of corrupting the stage.
		if filepath.Clean(contentDest) == filepath.Clean(stageDir) {
			return nil, fmt.Errorf("publish %s: --layout nested with a root-layout entry (path %q) would wipe the stage repo — use --layout root", e.Name, e.Path)
		}
		if err := os.RemoveAll(contentDest); err != nil {
			return nil, fmt.Errorf("clean nested dest: %w", err)
		}
	}

	if err := copyDir(editAbs, contentDest); err != nil {
		return nil, fmt.Errorf("copy skill into stage: %w", err)
	}

	// Stage + commit.
	if err := stagedWt.AddWithOptions(&gogit.AddOptions{All: true}); err != nil {
		return nil, fmt.Errorf("stage: %w", err)
	}
	stageStatus, err := stagedWt.Status()
	if err != nil {
		return nil, fmt.Errorf("stage status: %w", err)
	}
	return stageStatus, nil
}

// pushPublishStage creates the annotated tag (when tagName is set) and pushes
// the branch — atomically with the tag for tag publishes (issue #75) — rolling
// back a created tag if the push fails.
func (p *Publisher) pushPublishStage(ctx context.Context, stagedRepo *gogit.Repository, stageDir, branch, tagName string, commitHash plumbing.Hash, author, email string) error {
	// Push. Atomic for tag publishes (issue #75 — branch and tag either
	// both land or neither). For branch-only, single-refspec push goes
	// through the older non-atomic protocol for compatibility.
	refSpecs := []string{fmt.Sprintf("refs/heads/%s:refs/heads/%s", branch, branch)}
	var tagCreated bool
	if tagName != "" {
		tagRef := plumbing.NewTagReferenceName(tagName)
		if _, err := stagedRepo.Reference(tagRef, false); err == nil {
			return fmt.Errorf("tag %s already exists on remote — bump --tag for this skill", tagName)
		}
		if _, err := stagedRepo.CreateTag(tagName, commitHash, &gogit.CreateTagOptions{
			Tagger:  &object.Signature{Name: author, Email: email, When: time.Now()},
			Message: fmt.Sprintf("Release %s", tagName),
		}); err != nil {
			return fmt.Errorf("create tag: %w", err)
		}
		tagCreated = true
		refSpecs = append(refSpecs, fmt.Sprintf("refs/tags/%s:refs/tags/%s", tagName, tagName))
	}
	if err := p.Git.Push(ctx, stageDir, "origin", refSpecs); err != nil {
		if tagCreated {
			_ = stagedRepo.DeleteTag(tagName)
		}
		return fmt.Errorf("push: %w", err)
	}
	return nil
}

// publishInstalledDryRun mirrors the real publish's branch precedence so the
// reported target matches what the next real publish will do. We skip the clone
// (that's what "dry-run" buys you) but DO call ls-remote --symref — it's a
// refs-metadata round-trip, no blobs, and without it dry-run prints entry.Ref
// while the real publish goes to the symref-resolved branch (the user-reported
// divergence for issue #95). Precedence here matches the real path minus the
// stage-HEAD step (which requires the clone we're avoiding).
//
// Empty-remote detection (issue #113 follow-up): when ls-remote returns no refs
// the remote is fresh — entry.Ref (typically the source install's tag like
// "v0.1.0") can't be a real branch on it, so we skip the entry.Ref fallback and
// try to read the local bare's HEAD symref instead. Without this dry-run reports
// "would publish ...@v0.1.0" against an empty bare whose HEAD is actually `main`.
func (p *Publisher) publishInstalledDryRun(ctx context.Context, req PublishInstalledRequest, e *model.LockEntry, remoteURL, tagName, layout, editAbs, forkedFromValue string) *PublishInstalledResult {
	branch := req.Branch
	if branch == "" {
		if def, dErr := p.Git.RemoteDefaultBranch(ctx, remoteURL); dErr == nil && def != "" {
			branch = def
		}
	}
	dryRunEmpty := false
	if branch == "" {
		if refs, lerr := p.Git.LsRemote(ctx, remoteURL); lerr == nil && len(refs.Refs) == 0 {
			dryRunEmpty = true
		}
		if def := localBareDefaultBranch(remoteURL); def != "" {
			branch = def
			dryRunEmpty = true
		}
	}
	if branch == "" && !dryRunEmpty && e.Ref != "" {
		branch = e.Ref
	}
	if branch == "" {
		branch = "main"
	}
	return &PublishInstalledResult{
		Skill:        e.Name,
		Remote:       remoteURL,
		Branch:       branch,
		Tag:          tagName,
		DryRun:       true,
		ForkedFrom:   forkedFromValue,
		UpstreamPath: editAbs,
		Migrated:     req.ForkURL != "" && req.Migrate,
		Layout:       layout,
	}
}

// resolvePublishBranch determines the branch the real publish targets (issue #95).
//  1. --branch <X> explicit.
//  2. Stage clone's HEAD branch (== upstream's default by construction —
//     `git clone` checks it out). Cheap; already loaded.
//  3. Remote HEAD symref via `git ls-remote --symref` — authoritative when the
//     stage's HEAD is missing or detached (which happens on emptyOrNew,
//     fork-to-new-remote, and any host that doesn't include a symref header in
//     clone). One extra network round-trip but correct under upstream renames
//     (master → main).
//  4. (emptyOrNew only) Local bare's HEAD symref — `ls-remote` on a refs-less
//     bare returns nothing, but a local `git init --bare` still has HEAD →
//     refs/heads/main set in $GIT_DIR/HEAD. Reading it directly catches the
//     issue #113 case where the user pipes a fresh bare as --fork and we'd
//     otherwise fall through to entry.Ref (a tag from the source install).
//  5. entry.Ref (last-published label) — fallback only; stale by construction
//     once the upstream renames. Skipped for emptyOrNew because a fresh empty
//     repo has no concept of "last-published"; forwarding the source's install
//     ref (often `v0.1.0`) onto an empty bare pushes the very first commit onto
//     a branch named after a tag, which is never what the user wants (issue #113).
//  6. "main".
func (p *Publisher) resolvePublishBranch(ctx context.Context, req PublishInstalledRequest, e *model.LockEntry, remoteURL string, stagedRepo *gogit.Repository, emptyOrNew bool) string {
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
	if branch == "" && emptyOrNew {
		if def := localBareDefaultBranch(remoteURL); def != "" {
			branch = def
		}
	}
	if branch == "" && !emptyOrNew && e.Ref != "" {
		branch = e.Ref
	}
	if branch == "" {
		branch = "main"
	}
	return branch
}

// publishCommitIdentity resolves the author, email, and commit message for the
// publish commit, applying the qvr defaults when the request leaves them empty.
func publishCommitIdentity(req PublishInstalledRequest, e *model.LockEntry) (author, email, message string) {
	author = req.Author
	if author == "" {
		author = "quiver"
	}
	email = req.Email
	if email == "" {
		email = "quiver@localhost"
	}
	message = req.Message
	if message == "" {
		message = fmt.Sprintf("qvr: publish %s", e.Name)
		if req.Tag != "" {
			message = fmt.Sprintf("qvr: release %s %s", e.Name, req.Tag)
		}
	}
	return author, email, message
}

// commitPublishStage commits the staged content and returns the resulting hash.
// A dirty stage gets a normal commit; an empty-upstream stage with clean status
// gets a defensive empty initial commit so the branch exists on push; otherwise
// the existing HEAD hash is reused.
func commitPublishStage(stagedRepo *gogit.Repository, stagedWt *gogit.Worktree, stageStatus gogit.Status, emptyOrNew bool, author, email, message string) (plumbing.Hash, error) {
	commitHash := plumbing.ZeroHash
	switch {
	case !stageStatus.IsClean():
		h, cerr := stagedWt.Commit(message, &gogit.CommitOptions{
			Author: &object.Signature{Name: author, Email: email, When: time.Now()},
		})
		if cerr != nil {
			return plumbing.ZeroHash, fmt.Errorf("commit: %w", cerr)
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
			return plumbing.ZeroHash, fmt.Errorf("commit (empty init): %w", cerr)
		}
		commitHash = h
	default:
		if head, hErr := stagedRepo.Head(); hErr == nil {
			commitHash = head.Hash()
		}
	}
	return commitHash, nil
}

// finalizePublishedEntry mutates the lock entry in place after a successful
// push: Commit advances to the eject dir's HEAD, --fork --migrate flips
// Source/Registry and records the upstream + forkedFrom lineage in the
// provenance block, and SubtreeHash is recomputed.
func finalizePublishedEntry(e *model.LockEntry, req PublishInstalledRequest, editAbs, forkedFromValue string) {
	if head, hErr := readRepoHead(editAbs); hErr == nil && head != "" {
		e.Commit = head
	}
	if req.ForkURL != "" && req.Migrate {
		p := e.EnsureProvenance()
		if p.Upstream == "" {
			p.Upstream = e.Source
		}
		e.Source = req.ForkURL
		e.Registry = ""
		// Record fork provenance in the lockfile. forkedFromValue was
		// computed from the pre-publish upstream + eject HEAD, so it
		// captures "this fork was based on <upstream>@<sha>" — read at
		// trust-policy time in v0.9.
		if forkedFromValue != "" {
			p.ForkedFrom = forkedFromValue
		}
	}
	if h, hErr := canonical.HashSubtreeFromDisk(editAbs); hErr == nil {
		e.SubtreeHash = h
	}
}

// localBareDefaultBranch opens a local bare repo at url and returns the
// short branch name from its HEAD symref. Returns "" for non-local URLs,
// unreadable repos, non-branch HEADs (detached / tag symref), or any
// error — caller falls through to the next branch-resolution step.
//
// Why this exists (issue #113): a fresh `git init --bare` has HEAD
// pointing at refs/heads/main (or whatever init.defaultBranch is) but
// no refs to advertise, so `git ls-remote --symref` returns nothing.
// Without this helper the publisher falls through to entry.Ref — which
// is typically the source install's tag like "v0.1.0" — and pushes the
// very first commit onto a branch named after that tag. Reading
// $GIT_DIR/HEAD directly catches the local-bare case the network
// query can't see.
func localBareDefaultBranch(url string) string {
	path := localBarePath(url)
	if path == "" {
		return ""
	}
	repo, err := gogit.PlainOpen(path)
	if err != nil {
		return ""
	}
	head, err := repo.Reference(plumbing.HEAD, false)
	if err != nil {
		return ""
	}
	if head.Type() != plumbing.SymbolicReference {
		return ""
	}
	target := head.Target()
	if !target.IsBranch() {
		return ""
	}
	return target.Short()
}

// localBarePath returns the filesystem path corresponding to url when
// url refers to a local repository (an absolute/relative path or a
// file:// URL). Returns "" for ssh/https URLs and for paths that don't
// exist on disk.
func localBarePath(url string) string {
	if url == "" {
		return ""
	}
	const fileScheme = "file://"
	switch {
	case strings.HasPrefix(url, fileScheme):
		path := strings.TrimPrefix(url, fileScheme)
		if _, err := os.Stat(path); err != nil {
			return ""
		}
		return path
	case strings.HasPrefix(url, "/"), strings.HasPrefix(url, "./"), strings.HasPrefix(url, "../"):
		if _, err := os.Stat(url); err != nil {
			return ""
		}
		return url
	}
	// Bare relative path (no leading ./, e.g. "fork.git"). Treat as a
	// path only when it actually exists — refuses to misclassify
	// "user@host:repo" SSH URLs as local.
	if !strings.Contains(url, "://") && !strings.Contains(url, "@") {
		if _, err := os.Stat(url); err == nil {
			return url
		}
	}
	return ""
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

// ensureEditRepo guarantees the mode:edit dir is a git repo before publish.
// PlainOpen only inspects dir/.git (no parent search), so a `qvr create`'d dir
// nested inside a project repo is correctly seen as un-initialized and gets its
// own repo + initial commit rather than borrowing the project's. A pre-existing
// repo is left untouched. Issue #150.
func ensureEditRepo(dir, name string) error {
	if _, err := gogit.PlainOpen(dir); err == nil {
		return nil
	} else if !errors.Is(err, gogit.ErrRepositoryNotExists) {
		return fmt.Errorf("open edit repo %s: %w", dir, err)
	}
	if err := InitRepoWithCommit(dir, fmt.Sprintf("Initialize skill %s", name), "", ""); err != nil {
		return fmt.Errorf("initialize edit repo %s: %w", dir, err)
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

// Compile-time guard: keep git.GitClient referenced so go-mod-tidy doesn't
// strip the import when this file is the only one using its push contract
// transitively. The interface itself lives on Publisher.Git already; this
// is belt-and-braces against future refactors.
var _ git.GitClient = (*git.GoGitClient)(nil)
