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
	ErrAlreadyEditing  = errors.New("skill is already in edit mode")
	ErrCannotEditLink  = errors.New("cannot edit a link install — modify the source path directly")
	ErrCannotEjectLink = errors.New("cannot eject a link install")
	ErrNoTargets       = errors.New("entry has no installed targets to eject into")
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
	if len(e.Targets) == 0 {
		return nil, ErrNoTargets
	}

	canonicalTarget := pickCanonicalTarget(e.Targets)
	t, ok := model.LookupTarget(canonicalTarget)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownTarget, canonicalTarget)
	}

	// Path layout depends on scope (issue #82):
	//   --global    → canonical lives at <home>/<t.GlobalDir>/<name>; EditPath
	//                 is recorded as an absolute path so it resolves the same
	//                 regardless of cwd at later qvr invocations.
	//   project     → canonical lives at <projectRoot>/<t.LocalDir>/<name>;
	//                 EditPath is a project-relative path so the lockfile
	//                 remains portable across clones of the project.
	var canonicalAbs, editPathForLock string
	if req.Global {
		expanded, expErr := expandHome(t.GlobalDir)
		if expErr != nil {
			return nil, fmt.Errorf("expand global dir: %w", expErr)
		}
		canonicalAbs = filepath.Join(expanded, e.Name)
		editPathForLock = canonicalAbs
	} else {
		canonicalRel := filepath.Join(t.LocalDir, e.Name)
		canonicalAbs = filepath.Join(req.ProjectRoot, canonicalRel)
		editPathForLock = canonicalRel
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

	// Resolve the shared worktree source: where we'll copy the skill files
	// from. Falls back to LoadFromPath when the worktree isn't on disk so a
	// user who deleted ~/.quiver/worktrees/ before invoking edit gets a clean
	// error instead of an empty copy.
	sourceDir := EffectiveTarget(e, req.ProjectRoot)
	if sourceDir == "" {
		return nil, fmt.Errorf("eject %s: no source worktree to copy from — run `qvr sync` first", e.Name)
	}
	if _, err := os.Stat(filepath.Join(sourceDir, "SKILL.md")); err != nil {
		return nil, fmt.Errorf("eject %s: source %s does not contain SKILL.md: %w", e.Name, sourceDir, err)
	}

	// Stage to a sibling tmp dir, then rename onto canonical. Avoids leaving
	// half-copied state if the copy / git init fails midway.
	stagingDir := canonicalAbs + ".ejecting"
	_ = os.RemoveAll(stagingDir)
	if err := copyDir(sourceDir, stagingDir); err != nil {
		_ = os.RemoveAll(stagingDir)
		return nil, fmt.Errorf("copy skill tree: %w", err)
	}
	// The shared worktree is frozen read-only and copyDir preserves source
	// modes, so the freshly-copied edit tree would be read-only. Restore write
	// bits: this copy is the editable working dir, and initEjectRepo is about
	// to `git init` and write into it. This is the immutable→editable hinge.
	setSubtreeWritable(stagingDir)
	if err := initEjectRepo(stagingDir, e, req.Author, req.AuthorEmail); err != nil {
		_ = os.RemoveAll(stagingDir)
		return nil, fmt.Errorf("init edit repo: %w", err)
	}

	// Remove the existing canonical symlink (or no-op if it isn't there) so
	// the rename below lands on a clean slot. CreateSymlink earlier may have
	// pointed it at the shared worktree; if a real dir is there already we
	// refuse to clobber user content.
	if existing, err := os.Lstat(canonicalAbs); err == nil {
		if existing.Mode()&os.ModeSymlink == 0 {
			_ = os.RemoveAll(stagingDir)
			return nil, fmt.Errorf("eject %s: %s exists and is not a symlink — refuse to overwrite", e.Name, canonicalAbs)
		}
		if err := os.Remove(canonicalAbs); err != nil {
			_ = os.RemoveAll(stagingDir)
			return nil, fmt.Errorf("remove canonical symlink: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(canonicalAbs), 0o755); err != nil {
		_ = os.RemoveAll(stagingDir)
		return nil, fmt.Errorf("create canonical parent: %w", err)
	}
	if err := os.Rename(stagingDir, canonicalAbs); err != nil {
		_ = os.RemoveAll(stagingDir)
		return nil, fmt.Errorf("finalize edit dir: %w", err)
	}

	// Repoint sibling targets at the canonical via relative symlinks.
	// CreateSymlink would write absolute paths; for sibling links we want
	// them relative so the lockfile-as-checked-in property survives a
	// `git clone` to a different absolute project location.
	//
	// Sibling targets pick their dir from the same scope as the canonical:
	// --global ejects place sibling symlinks under each sibling's GlobalDir
	// (e.g. ~/.cursor/rules/<name>), project ejects under each LocalDir.
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

	// Mutate the entry on success.
	e.Mode = model.ModeEdit
	e.EditPath = editPathForLock
	if e.SourceUpstream == "" {
		e.SourceUpstream = e.Source
	}
	// Refresh subtree hash against the newly materialised dir so drift
	// detection has a current baseline. HashSubtreeFromDisk now excludes
	// .git/ so the hash matches what `qvr lock verify` later recomputes
	// (issue #80). Also re-seal entry.Commit to the new edit-repo HEAD —
	// the eject created a fresh git history, so the upstream commit no
	// longer applies, and leaving it would surface as drift on the next
	// verify (issue #73 / #74).
	if h, err := canonical.HashSubtreeFromDisk(canonicalAbs); err == nil {
		e.SubtreeHash = h
	}
	if head, hErr := readRepoHead(canonicalAbs); hErr == nil && head != "" {
		e.Commit = head
	}

	return &EjectResult{
		Skill:           e.Name,
		CanonicalTarget: canonicalTarget,
		EditPath:        editPathForLock,
		SiblingLinks:    siblingLinks,
	}, nil
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
// and writes a single commit. Shared by the worktree-eject path and `qvr init`
// so every mode:edit skill is backed by a real repo from birth — without this
// an `qvr init`'d skill had no .git/ and `qvr publish --fork` aborted with the
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
