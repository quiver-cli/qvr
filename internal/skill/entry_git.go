package skill

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/quiver-cli/qvr/internal/git"
	"github.com/quiver-cli/qvr/internal/model"
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
//
// Implementation: shells out to `git merge-base --is-ancestor` because go-git's
// CommitObject.IsAncestor was returning false-negatives on freshly-init'd eject
// repos (the synthetic eject commit + user-added commits via system git),
// forcing users to pass --allow-lockfile-heal on every publish (issue #99).
// System git is the same binary the user used to make the commit in the first
// place — answers stay consistent by construction. Falls back to the go-git
// path only when the git binary isn't available.
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
	// Primary path: shell out to system git. Exit 0 → ancestor; exit 1 →
	// not an ancestor (also covers "entry.Commit isn't a known object" —
	// the #74 case). Any other failure (no git binary, no repo) falls
	// through to go-git below so the function stays usable in mock-driven
	// tests that don't have a real git installed.
	ancestor, err := git.IsAncestor(context.Background(), repoPath, entry.Commit, "HEAD")
	if err == nil {
		return ancestor, nil
	}
	// Fallback path: go-git, kept for environments without a git binary.
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
	entryCommit, err := repo.CommitObject(entryHash)
	if err != nil {
		return false, nil
	}
	if headCommit.Hash == entryCommit.Hash {
		return true, nil
	}
	return entryCommit.IsAncestor(headCommit)
}
