package skill_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/raks097/quiver/internal/skill"
)

// TestEntryCommitIsAncestorOfHead_LocalCommit covers issue #99: after the user
// makes a real git commit inside an ejected dir, the lockfile's commit field
// still points at the eject base. That's not tampering — head descends from
// entry.Commit. The helper must report ancestor=true so publish/verify can
// silently heal instead of nagging for --allow-lockfile-heal on every push.
func TestEntryCommitIsAncestorOfHead_LocalCommit(t *testing.T) {
	entry, projectRoot, editDir := ejectedFixture(t, "demo")

	// Capture the eject-base commit before advancing HEAD.
	repo, err := gogit.PlainOpen(editDir)
	if err != nil {
		t.Fatalf("open edit repo: %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	baseSHA := head.Hash().String()
	entry.Commit = baseSHA // make the entry honest before advancing

	// Make a new commit on top.
	if err := os.WriteFile(filepath.Join(editDir, "extra.md"), []byte("edit\n"), 0o644); err != nil {
		t.Fatalf("write extra: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := wt.AddWithOptions(&gogit.AddOptions{All: true}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := wt.Commit("user edit", &gogit.CommitOptions{
		Author: &object.Signature{Name: "u", Email: "u@e", When: time.Now()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	ok, err := skill.EntryCommitIsAncestorOfHead(entry, projectRoot)
	if err != nil {
		t.Fatalf("EntryCommitIsAncestorOfHead: %v", err)
	}
	if !ok {
		t.Errorf("ancestor=false, want true: entry.Commit %s should be reachable from HEAD after a normal local commit", entry.Commit)
	}
}

// TestEntryCommitIsAncestorOfHead_TamperedCommit covers issue #74: a lockfile
// commit field hand-edited to a SHA that has never existed in the repo must
// NOT be silently treated as a legitimate ancestor. The helper returns
// (false, nil), routing publish into the --allow-lockfile-heal refusal path.
func TestEntryCommitIsAncestorOfHead_TamperedCommit(t *testing.T) {
	entry, projectRoot, _ := ejectedFixture(t, "demo")
	entry.Commit = "deadbeef00000000000000000000000000000000"

	ok, err := skill.EntryCommitIsAncestorOfHead(entry, projectRoot)
	if err != nil {
		t.Fatalf("EntryCommitIsAncestorOfHead: %v", err)
	}
	if ok {
		t.Errorf("ancestor=true for nonexistent commit, want false (issue #74)")
	}
}
