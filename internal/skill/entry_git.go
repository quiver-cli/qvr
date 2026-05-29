package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/raks097/quiver/internal/model"
)

// ResolveSkillRepoPath returns the on-disk path to the git repository that
// authoritatively describes the entry's current state.
//
// For mode:edit entries this is the edit dir (which holds a real .git/).
// For shared entries this is the bare-clone worktree under ~/.quiver/.
// For link installs there is no upstream repo; returns "" with no error.
//
// projectRoot is used only for resolving relative EditPath values; pass "" if
// no project root is in scope (the caller's cwd will be used as a fallback for
// relative paths).
func ResolveSkillRepoPath(entry *model.LockEntry, projectRoot string) string {
	if entry == nil {
		return ""
	}
	if entry.IsLink() {
		return ""
	}
	if entry.IsEdit() {
		if entry.EditPath == "" {
			return ""
		}
		if filepath.IsAbs(entry.EditPath) {
			return entry.EditPath
		}
		if projectRoot == "" {
			return entry.EditPath
		}
		return filepath.Join(projectRoot, entry.EditPath)
	}
	return EntryWorktreePath(entry)
}

// ResolveEntryHeadCommit reads the current HEAD SHA from the entry's
// authoritative git repo. Used by `qvr lock verify`, `qvr publish` and
// `qvr info` to cross-check that entry.Commit matches the on-disk reality
// (issue #73 / #74) — without this check, a hand-tampered `commit` field
// passes every audit qvr offers.
//
// Returns ("", nil) for link installs and for shared entries whose worktree
// is missing (the caller treats that as "no signal to compare").
// Returns ("", err) when the repo exists but cannot be opened or has no HEAD.
func ResolveEntryHeadCommit(entry *model.LockEntry, projectRoot string) (string, error) {
	if entry == nil {
		return "", errors.New("nil entry")
	}
	if entry.IsLink() {
		return "", nil
	}
	repoPath := ResolveSkillRepoPath(entry, projectRoot)
	if repoPath == "" {
		return "", nil
	}
	if _, err := os.Stat(repoPath); err != nil {
		return "", nil
	}
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return "", fmt.Errorf("open repo at %s: %w", repoPath, err)
	}
	head, err := repo.Head()
	if err != nil {
		return "", fmt.Errorf("head at %s: %w", repoPath, err)
	}
	return head.Hash().String(), nil
}

// EntryCommitIsAncestorOfHead reports whether entry.Commit is reachable from
// the repo's current HEAD. Used by publish + lock-verify to tell apart
// "user committed legitimately on top of the recorded commit" (issue #99,
// silent heal is fine) from "lockfile commit field is a fabrication that
// has never existed in this repo" (issue #74, require explicit --allow-lockfile-heal).
//
// Returns (false, nil) if either commit is missing from the repo or the
// repo isn't openable — callers treat that as "no signal, fall back to
// the strict equality check".
func EntryCommitIsAncestorOfHead(entry *model.LockEntry, projectRoot string) (bool, error) {
	if entry == nil || entry.Commit == "" {
		return false, nil
	}
	if entry.IsLink() {
		return false, nil
	}
	repoPath := ResolveSkillRepoPath(entry, projectRoot)
	if repoPath == "" {
		return false, nil
	}
	if _, err := os.Stat(repoPath); err != nil {
		return false, nil
	}
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return false, nil
	}
	head, err := repo.Head()
	if err != nil {
		return false, nil
	}
	headCommit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return false, nil
	}
	entryHash := plumbing.NewHash(entry.Commit)
	// plumbing.NewHash accepts any 40-char hex; if entry.Commit is
	// "deadbeef..." it returns that literal hash, which won't resolve
	// to an object in the repo. CommitObject fails → ancestor=false,
	// which correctly routes the caller into the #74 refusal path.
	entryCommit, err := repo.CommitObject(entryHash)
	if err != nil {
		return false, nil
	}
	if headCommit.Hash == entryCommit.Hash {
		return true, nil
	}
	// IsAncestor(target) reports whether the receiver is an ancestor of
	// `target` — i.e. whether `target` descends from the receiver. We
	// want: does HEAD descend from entry.Commit?
	return entryCommit.IsAncestor(headCommit)
}
