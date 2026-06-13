package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/astra-sh/qvr/internal/canonical"
	"github.com/astra-sh/qvr/internal/model"
)

var (
	// ErrAlreadyEditing is returned by Eject when the lock entry already
	// records an EditPath — the skill is already ejected for editing.
	ErrAlreadyEditing = errors.New("skill is already in edit mode")
	// ErrCannotEditLink means `qvr edit` was asked for a link install:
	// the source path is already writable, so there is nothing to eject.
	ErrCannotEditLink = errors.New("cannot edit a link install — modify the source path directly")
	// ErrCannotEjectLink is returned by Eject when the lock entry is a
	// link install — link installs have no immutable worktree to copy out.
	ErrCannotEjectLink = errors.New("cannot eject a link install")
	// ErrNoTargets is returned by Eject when the lock entry records no
	// installed targets, leaving no agent dir to eject the copy into.
	ErrNoTargets = errors.New("entry has no installed targets to eject into")
	// ErrCannotVendorLink is returned by VendorIntoRepo for a link install:
	// its bytes live at an external path with no store worktree to copy in.
	ErrCannotVendorLink = errors.New("cannot vendor a link install")
	// ErrCannotEjectVendor is returned by EjectToTarget for a vendored entry:
	// its files are already real and writable in the repo, so there is nothing
	// to eject.
	ErrCannotEjectVendor = errors.New("cannot eject a vendored install — its files are already editable in the repo")
)

// EjectRequest drives a `qvr edit` eject.
type EjectRequest struct {
	Entry       *model.LockEntry // the lock entry to eject (mutated in place on success)
	ProjectRoot string           // absolute project root
	// Global, when true, ejects into the user-global agent dir (e.g.
	// ~/.claude/skills/<name>) and writes EditPath as an absolute path.
	// When false (default), ejects into the project-scoped dir
	// (.claude/skills/<name>) and writes a project-relative EditPath.
	// Fixes issue #82 — without this, --global eject silently landed in
	// cwd and left the global lane untouched.
	Global bool
	// Author and AuthorEmail are stamped on the initial git commit inside
	// the new edit dir. Empty values fall back to "quiver"/"quiver@localhost".
	Author      string
	AuthorEmail string
}

// EjectResult summarises an eject for the caller.
type EjectResult struct {
	Skill           string   `json:"skill"`
	CanonicalTarget string   `json:"canonical_target"`
	EditPath        string   `json:"edit_path"`     // project-relative
	SiblingLinks    []string `json:"sibling_links"` // absolute paths of repointed sibling symlinks
}

// EjectToTarget promotes the shared-worktree symlink at the alphabetical-first
// installed target into a real directory, copies the skill subtree into it,
// initialises a fresh git history at the upstream HEAD, and repoints any
// other installed targets at the canonical via relative symlinks.
//
// The lock entry is mutated in place: Mode flips to "edit", EditPath is set
// to the project-relative canonical path, and SourceUpstream captures the
// previous Source so provenance survives a later `qvr publish --fork --migrate`
// rewrite of Source.
//
// Idempotent: a second invocation when Mode is already "edit" returns the
// existing canonical/edit-path with no filesystem mutation. Refuses link
// installs.
func EjectToTarget(req EjectRequest) (*EjectResult, error) {
	e := req.Entry
	if e == nil {
		return nil, errors.New("eject: nil entry")
	}
	if req.ProjectRoot == "" {
		return nil, errors.New("eject: project root is required")
	}
	if e.IsLink() {
		return nil, ErrCannotEjectLink
	}
	if e.IsVendor() {
		// A vendored skill is already real, writable, in-repo files — there is
		// no store worktree to eject and the canonical target is a real dir, so
		// the eject rename would refuse to clobber it. Reject explicitly.
		return nil, ErrCannotEjectVendor
	}
	if len(e.Targets) == 0 {
		return nil, ErrNoTargets
	}

	canonicalTarget := pickCanonicalTarget(e.Targets)
	t, ok := model.LookupTarget(canonicalTarget)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownTarget, canonicalTarget)
	}

	canonicalAbs, editPathForLock, err := ejectCanonicalPaths(t, e, req)
	if err != nil {
		return nil, err
	}

	// Idempotent no-op when the entry already records this exact eject.
	if e.IsEdit() {
		if e.EditPath == editPathForLock {
			return &EjectResult{
				Skill:           e.Name,
				CanonicalTarget: canonicalTarget,
				EditPath:        editPathForLock,
			}, nil
		}
		return nil, fmt.Errorf("%w (current canonical: %s)", ErrAlreadyEditing, e.EditPath)
	}

	if err := materializeEjectDir(e, req, canonicalAbs); err != nil {
		return nil, err
	}

	siblingLinks, err := repointSiblingTargets(e, req, canonicalTarget, canonicalAbs)
	if err != nil {
		// materializeEjectDir already created the canonical real directory; a
		// sibling-relink failure must not leave it behind. rollbackLinks (in the
		// caller) only removes symlinks, so the real dir would orphan and block
		// future installs of this skill. Remove it here to keep eject/vendor
		// atomic — the caller still rolls back the target symlinks.
		_ = os.RemoveAll(canonicalAbs)
		return nil, err
	}

	finalizeEjectedEntry(e, editPathForLock, canonicalAbs)

	return &EjectResult{
		Skill:           e.Name,
		CanonicalTarget: canonicalTarget,
		EditPath:        editPathForLock,
		SiblingLinks:    siblingLinks,
	}, nil
}

// materializeEjectDir resolves the shared-worktree source, copies the skill
// subtree into a staging sibling dir, restores write bits (the immutable→editable
// hinge), inits a fresh git history, then atomically renames the staging dir onto
// the canonical slot. Thin wrapper over copyTreeToCanonical with the edit-mode
// settings (nested git history seeded).
func materializeEjectDir(e *model.LockEntry, req EjectRequest, canonicalAbs string) error {
	return copyTreeToCanonical(e, req.ProjectRoot, canonicalAbs, ".ejecting", true, req.Author, req.AuthorEmail)
}

// copyTreeToCanonical resolves the entry's current effective source (the shared
// store worktree or the local copy), copies it into a staging sibling of
// canonicalAbs, restores write bits, optionally seeds a fresh nested git history,
// then atomically renames the staging dir onto the canonical agent-dir slot —
// refusing to clobber a real (non-symlink) dir there. The staging-then-rename
// avoids leaving half-copied state if any step fails midway.
//
// Shared by `qvr edit` (initNestedRepo=true: the eject dir is a standalone fork
// that `qvr publish` later pushes) and `qvr add --vendor` (initNestedRepo=false:
// the bytes are tracked by the OUTER project repo, so a nested .git would be both
// redundant and a committed-repo-inside-a-repo footgun).
func copyTreeToCanonical(e *model.LockEntry, projectRoot, canonicalAbs, stagingSuffix string, initNestedRepo bool, author, authorEmail string) error {
	// Resolve the source: where we'll copy the skill files from. Returns ""
	// when the worktree isn't on disk so a user who deleted ~/.quiver/worktrees/
	// gets a clean error instead of an empty copy.
	sourceDir := EffectiveTarget(e, projectRoot)
	if sourceDir == "" {
		return fmt.Errorf("%s: no source worktree to copy from — run `qvr sync` first", e.Name)
	}
	if _, err := os.Stat(filepath.Join(sourceDir, "SKILL.md")); err != nil {
		return fmt.Errorf("%s: source %s does not contain SKILL.md: %w", e.Name, sourceDir, err)
	}

	// Stage to a sibling tmp dir, then rename onto canonical. Avoids leaving
	// half-copied state if the copy / git init fails midway.
	stagingDir := canonicalAbs + stagingSuffix
	_ = os.RemoveAll(stagingDir)
	if err := copyDir(sourceDir, stagingDir); err != nil {
		_ = os.RemoveAll(stagingDir)
		return fmt.Errorf("copy skill tree: %w", err)
	}
	// The source worktree is frozen read-only and copyDir preserves source
	// modes, so the freshly-copied tree would be read-only. Restore write bits:
	// this copy is the working dir the user (and any `git init` below) writes to.
	setSubtreeWritable(stagingDir)
	if initNestedRepo {
		if err := initEjectRepo(stagingDir, e, author, authorEmail); err != nil {
			_ = os.RemoveAll(stagingDir)
			return fmt.Errorf("init edit repo: %w", err)
		}
	}
	return promoteStagingOntoCanonical(e, stagingDir, canonicalAbs)
}

// promoteStagingOntoCanonical removes the existing canonical symlink (or no-op
// if absent) so the rename lands on a clean slot, then atomically renames the
// staging dir onto it. CreateSymlink earlier may have pointed the canonical at
// the shared worktree; if a real dir is there already we refuse to clobber user
// content. On any failure the staging dir is removed so no half-state lingers.
func promoteStagingOntoCanonical(e *model.LockEntry, stagingDir, canonicalAbs string) error {
	if existing, err := os.Lstat(canonicalAbs); err == nil {
		if existing.Mode()&os.ModeSymlink == 0 {
			_ = os.RemoveAll(stagingDir)
			return fmt.Errorf("%s: %s exists and is not a symlink — refuse to overwrite", e.Name, canonicalAbs)
		}
		if err := os.Remove(canonicalAbs); err != nil {
			_ = os.RemoveAll(stagingDir)
			return fmt.Errorf("remove canonical symlink: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(canonicalAbs), 0o755); err != nil {
		_ = os.RemoveAll(stagingDir)
		return fmt.Errorf("create canonical parent: %w", err)
	}
	if err := os.Rename(stagingDir, canonicalAbs); err != nil {
		_ = os.RemoveAll(stagingDir)
		return fmt.Errorf("finalize canonical dir: %w", err)
	}
	return nil
}

// VendorRequest drives a `qvr add --vendor` post-install vendoring step.
type VendorRequest struct {
	Entry       *model.LockEntry // the freshly-installed lock entry (mutated in place on success)
	ProjectRoot string           // absolute project root
	// Global, when true, vendors into the user-global agent dir and writes
	// VendorPath as an absolute path; project scope (default) writes a
	// project-relative VendorPath so the lockfile stays portable across clones.
	Global bool
}

// VendorResult summarises a vendoring for the caller.
type VendorResult struct {
	Skill           string   `json:"skill"`
	CanonicalTarget string   `json:"canonical_target"`
	VendorPath      string   `json:"vendor_path"`   // project-relative (or absolute when Global)
	SiblingLinks    []string `json:"sibling_links"` // absolute paths of repointed sibling symlinks
}

// VendorIntoRepo promotes a freshly-installed skill's store worktree into a real
// directory committed into the repo at the alphabetical-first installed target,
// and repoints any other installed targets at it via relative symlinks. The lock
// entry is mutated in place: Mode flips to "vendor", VendorPath records the
// project-relative canonical path, and SubtreeHash is re-sealed against the
// in-repo dir.
//
// Unlike EjectToTarget it seeds NO nested git history — the vendored bytes are
// tracked by the OUTER project repo, which is exactly what lets the skill travel
// with a `git clone` (no store, no registry, no qvr needed to read it). It is the
// `--vendor` counterpart to a normal symlink-into-store install.
//
// Idempotent: a second call when Mode is already "vendor" at the same path is a
// no-op. Refuses link installs (no store worktree to copy from).
func VendorIntoRepo(req VendorRequest) (*VendorResult, error) {
	e := req.Entry
	if e == nil {
		return nil, errors.New("vendor: nil entry")
	}
	if req.ProjectRoot == "" {
		return nil, errors.New("vendor: project root is required")
	}
	if e.IsLink() {
		return nil, ErrCannotVendorLink
	}
	if len(e.Targets) == 0 {
		return nil, ErrNoTargets
	}

	canonicalTarget := pickCanonicalTarget(e.Targets)
	t, ok := model.LookupTarget(canonicalTarget)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownTarget, canonicalTarget)
	}

	// Reuse the eject path-derivation: --global → absolute canonical/VendorPath,
	// project → project-relative. The EjectRequest carries only scope here.
	scope := EjectRequest{ProjectRoot: req.ProjectRoot, Global: req.Global}
	canonicalAbs, vendorPathForLock, err := ejectCanonicalPaths(t, e, scope)
	if err != nil {
		return nil, err
	}

	// Idempotent no-op when the entry already records this exact vendoring.
	if e.IsVendor() && e.VendorPath == vendorPathForLock {
		return &VendorResult{Skill: e.Name, CanonicalTarget: canonicalTarget, VendorPath: vendorPathForLock}, nil
	}

	if err := copyTreeToCanonical(e, req.ProjectRoot, canonicalAbs, ".vendoring", false, "", ""); err != nil {
		return nil, err
	}

	siblingLinks, err := repointSiblingTargets(e, scope, canonicalTarget, canonicalAbs)
	if err != nil {
		return nil, err
	}

	finalizeVendoredEntry(e, vendorPathForLock, canonicalAbs)

	return &VendorResult{
		Skill:           e.Name,
		CanonicalTarget: canonicalTarget,
		VendorPath:      vendorPathForLock,
		SiblingLinks:    siblingLinks,
	}, nil
}

// finalizeVendoredEntry mutates the entry on success: flips it to mode:vendor,
// records the VendorPath, and re-seals SubtreeHash against the in-repo dir so
// drift detection (`qvr lock verify`) has a current baseline. The copy is
// byte-identical to the store worktree, so the hash normally matches what the
// install already recorded; re-hashing is defensive and matches eject's pattern.
// HashSubtreeFromDisk excludes .git/, so the seal agrees with a later verify.
func finalizeVendoredEntry(e *model.LockEntry, vendorPathForLock, canonicalAbs string) {
	e.Mode = model.ModeVendor
	e.VendorPath = vendorPathForLock
	if h, err := canonical.HashSubtreeFromDisk(canonicalAbs); err == nil {
		e.SubtreeHash = h
	}
}

// ejectCanonicalPaths computes the canonical eject dir and the EditPath recorded
// in the lock, scoped per issue #82:
//
//	--global    → canonical lives at <home>/<t.GlobalDir>/<name>; EditPath is an
//	              absolute path so it resolves the same regardless of cwd at
//	              later qvr invocations.
//	project     → canonical lives at <projectRoot>/<t.LocalDir>/<name>; EditPath
//	              is a project-relative path so the lockfile stays portable
//	              across clones of the project.
func ejectCanonicalPaths(t model.Target, e *model.LockEntry, req EjectRequest) (canonicalAbs, editPathForLock string, err error) {
	if req.Global {
		expanded, expErr := expandHome(t.GlobalDir)
		if expErr != nil {
			return "", "", fmt.Errorf("expand global dir: %w", expErr)
		}
		canonicalAbs = filepath.Join(expanded, e.Name)
		return canonicalAbs, canonicalAbs, nil
	}
	canonicalRel := filepath.Join(t.LocalDir, e.Name)
	canonicalAbs = filepath.Join(req.ProjectRoot, canonicalRel)
	return canonicalAbs, canonicalRel, nil
}

// repointSiblingTargets repoints every non-canonical target at the canonical
// edit dir via relative symlinks. CreateSymlink would write absolute paths; for
// sibling links we want them relative so the lockfile-as-checked-in property
// survives a `git clone` to a different absolute project location. Sibling
// targets pick their dir from the same scope as the canonical: --global places
// sibling symlinks under each sibling's GlobalDir (e.g. ~/.cursor/rules/<name>),
// project under each LocalDir. Returns the created sibling link paths.
func repointSiblingTargets(e *model.LockEntry, req EjectRequest, canonicalTarget, canonicalAbs string) ([]string, error) {
	var siblingLinks []string
	for _, target := range e.Targets {
		if target == canonicalTarget {
			continue
		}
		st, ok := model.LookupTarget(target)
		if !ok {
			continue
		}
		var siblingAbs string
		if req.Global {
			expanded, expErr := expandHome(st.GlobalDir)
			if expErr != nil {
				return nil, fmt.Errorf("expand sibling global dir for %s: %w", target, expErr)
			}
			siblingAbs = filepath.Join(expanded, e.Name)
		} else {
			siblingRel := filepath.Join(st.LocalDir, e.Name)
			siblingAbs = filepath.Join(req.ProjectRoot, siblingRel)
		}
		if err := os.MkdirAll(filepath.Dir(siblingAbs), 0o755); err != nil {
			return nil, fmt.Errorf("create sibling parent for %s: %w", target, err)
		}
		// Replace any existing symlink; refuse to clobber a real dir.
		if existing, lerr := os.Lstat(siblingAbs); lerr == nil {
			if existing.Mode()&os.ModeSymlink == 0 {
				return nil, fmt.Errorf("eject %s: sibling %s is not a symlink — refuse to overwrite", e.Name, siblingAbs)
			}
			if err := os.Remove(siblingAbs); err != nil {
				return nil, fmt.Errorf("remove sibling symlink: %w", err)
			}
		}
		relTarget, err := filepath.Rel(filepath.Dir(siblingAbs), canonicalAbs)
		if err != nil {
			// Fall back to absolute on Rel failure — uncommon, but better than refusing.
			relTarget = canonicalAbs
		}
		if err := os.Symlink(relTarget, siblingAbs); err != nil {
			return nil, fmt.Errorf("create sibling symlink %s: %w", siblingAbs, err)
		}
		siblingLinks = append(siblingLinks, siblingAbs)
	}
	return siblingLinks, nil
}

// finalizeEjectedEntry mutates the entry on success: flips it to mode:edit,
// records the EditPath, preserves the upstream source, and re-seals SubtreeHash
// and Commit against the freshly-materialised edit dir so drift detection has a
// current baseline (issues #80, #73/#74). HashSubtreeFromDisk excludes .git/ so
// the hash matches what `qvr lock verify` later recomputes.
func finalizeEjectedEntry(e *model.LockEntry, editPathForLock, canonicalAbs string) {
	e.Mode = model.ModeEdit
	e.EditPath = editPathForLock
	if p := e.EnsureProvenance(); p.Upstream == "" {
		p.Upstream = e.Source
	}
	if h, err := canonical.HashSubtreeFromDisk(canonicalAbs); err == nil {
		e.SubtreeHash = h
	}
	if head, hErr := readRepoHead(canonicalAbs); hErr == nil && head != "" {
		e.Commit = head
	}
}

// readRepoHead is a small helper for the eject path — go-git's PlainOpen +
// Head() boiled down to a single call. Returns "" without an error when the
// repo has no HEAD yet (shouldn't happen after initEjectRepo's commit but
// defensive).
func readRepoHead(repoPath string) (string, error) {
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return "", err
	}
	head, err := repo.Head()
	if err != nil {
		return "", err
	}
	return head.Hash().String(), nil
}

// pickCanonicalTarget returns the alphabetical-first installed target. The
// choice is deterministic so two invocations on the same lock entry pick the
// same canonical, and so consumers reading the lock can predict where the
// edit dir lives without re-running the algorithm.
func pickCanonicalTarget(targets []string) string {
	sorted := append([]string(nil), targets...)
	sort.Strings(sorted)
	return sorted[0]
}

// initEjectRepo seeds a fresh git history inside the staging dir so future
// `qvr publish` calls have something to commit + push. The initial commit
// captures the current upstream content and stamps the provenance reference
// in its message so `git log` shows where this fork originated.
func initEjectRepo(dir string, e *model.LockEntry, author, authorEmail string) error {
	upstreamRef := e.Source
	if upstreamRef == "" {
		upstreamRef = "<unknown>"
	}
	short := e.Commit
	if len(short) > 7 {
		short = short[:7]
	}
	msg := fmt.Sprintf("Eject %s from %s@%s", e.Name, upstreamRef, short)
	return InitRepoWithCommit(dir, msg, author, authorEmail)
}

// InitRepoWithCommit initializes a fresh git repo at dir, stages everything,
// and writes a single commit. Shared by the worktree-eject path and `qvr create`
// so every mode:edit skill is backed by a real repo from birth — without this
// a `qvr create`'d skill had no .git/ and `qvr publish --fork` aborted with the
// opaque "open: repository does not exist" (issue #150). author/authorEmail
// fall back to quiver defaults when blank. Errors if dir is already a repo.
func InitRepoWithCommit(dir, message, author, authorEmail string) error {
	if author == "" {
		author = "quiver"
	}
	if authorEmail == "" {
		authorEmail = "quiver@localhost"
	}
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		return fmt.Errorf("git init: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("worktree: %w", err)
	}
	if err := wt.AddWithOptions(&gogit.AddOptions{All: true}); err != nil {
		return fmt.Errorf("stage: %w", err)
	}
	if _, err := wt.Commit(message, &gogit.CommitOptions{
		Author: &object.Signature{Name: author, Email: authorEmail, When: time.Now()},
	}); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
