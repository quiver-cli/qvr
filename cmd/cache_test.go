package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/registry"
)

// resetPrinter wires the package-global `printer` to a discard sink for tests
// that call RunE functions directly.
func resetPrinter(t *testing.T) {
	t.Helper()
	prev := printer
	printer = &output.Printer{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, Format: output.FormatText}
	t.Cleanup(func() { printer = prev })
}

// fakeWorktree creates a directory under registry.WorktreesRoot() that looks
// like a real worktree to collectCacheEntries: a `.git` dir plus a payload
// file so dirSize > 0.
func fakeWorktree(t *testing.T, segments ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{registry.WorktreesRoot()}, segments...)...)
	if err := os.MkdirAll(filepath.Join(path, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	return path
}

// recordProjectWithLock writes a lock file at projectRoot/qvr.lock with a
// v5 entry whose EntryWorktreePath derivation (registry+name+
// ShortSHA(commit)) matches the path fakeWorktree(t, "acme", "demo",
// "abc1234") produces, then records the project so reachability sees it.
// Callers pass the seeded worktree path purely as a sanity hook — the
// derivation is authoritative.
func recordProjectWithLock(t *testing.T, projectRoot string) string {
	t.Helper()
	lockPath := filepath.Join(projectRoot, model.LockFileName)
	lock := model.NewLockFile(lockPath)
	lock.Put(&model.LockEntry{
		Name:     "demo",
		Registry: "acme",
		Source:   "git@example.test:acme.git",
		Ref:      "main",
		// The commit lines up with fakeWorktree(t, "acme", "demo",
		// "abc1234") so EntryWorktreePath resolves to the live worktree.
		Commit: "abc1234abcdef",
	})
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	registry.TouchProject(lockPath)
	return lockPath
}

func TestCollectCacheEntries_FlagsOrphansAndReachables(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)

	live := fakeWorktree(t, "acme", "demo", "abc1234")
	orphan := fakeWorktree(t, "acme", "demo", "deadbee")

	proj := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("mkdir proj: %v", err)
	}
	recordProjectWithLock(t, proj)

	entries, missing, err := collectCacheEntries()
	if err != nil {
		t.Fatalf("collectCacheEntries: %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("expected no missing projects, got %v", missing)
	}

	byPath := map[string]CacheEntry{}
	for _, e := range entries {
		byPath[e.Path] = e
	}
	if e, ok := byPath[live]; !ok || !e.Reachable {
		t.Errorf("live worktree not flagged reachable: %+v", e)
	}
	if e, ok := byPath[orphan]; !ok || e.Reachable {
		t.Errorf("orphan worktree not flagged orphan: %+v", e)
	}
}

func TestRunCachePrune_DryRunDoesNotDelete(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)

	orphan := fakeWorktree(t, "acme", "demo", "deadbee")
	_ = fakeWorktree(t, "acme", "demo", "abc1234") // live worktree the lock points at
	proj := filepath.Join(t.TempDir(), "proj")
	_ = os.MkdirAll(proj, 0o755)
	recordProjectWithLock(t, proj)

	// Wire up the printer for the test — runCachePrune touches it.
	resetPrinter(t)

	cachePruneDryRun = true
	t.Cleanup(func() { cachePruneDryRun = false })

	if err := runCachePrune(nil, nil); err != nil {
		t.Fatalf("runCachePrune dry-run: %v", err)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Errorf("dry-run should have left orphan in place: %v", err)
	}
}

func TestRunCachePrune_DeletesOrphans(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)

	orphan := fakeWorktree(t, "acme", "demo", "deadbee")
	live := fakeWorktree(t, "acme", "demo", "abc1234")
	proj := filepath.Join(t.TempDir(), "proj")
	_ = os.MkdirAll(proj, 0o755)
	recordProjectWithLock(t, proj)

	resetPrinter(t)
	cachePruneDryRun = false

	if err := runCachePrune(nil, nil); err != nil {
		t.Fatalf("runCachePrune: %v", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("orphan should be gone, got err=%v", err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("reachable worktree should survive, got err=%v", err)
	}
}

// TestRunCachePrune_ReturnsErrorOnDeleteFailure pins the contract that a
// partial-failure prune does NOT exit 0 — a CI script wrapping `qvr cache
// prune` must be able to detect that some orphans couldn't be removed.
// Achieved by making one of the orphan paths unremovable: chmod the parent
// dir to read-only so RemoveAll fails on the leaf.
func TestRunCachePrune_ReturnsErrorOnDeleteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses chmod permissions")
	}
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)

	// Build an orphan inside a parent we'll lock down.
	parent := filepath.Join(home, "worktrees", "acme", "demo")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	orphan := fakeWorktree(t, "acme", "demo", "deadbee")
	// Make parent read-only so RemoveAll(orphan) fails — leaf still
	// readable, but rmdir of the leaf needs write+exec on parent.
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatalf("chmod parent: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	resetPrinter(t)
	cachePruneDryRun = false
	err := runCachePrune(nil, nil)
	if err == nil {
		t.Fatal("expected error on delete failure, got nil")
	}
	// The orphan should still be on disk (delete failed).
	if _, statErr := os.Stat(orphan); statErr != nil {
		t.Fatalf("orphan unexpectedly removed even though delete failed: %v", statErr)
	}
}

func TestRunCachePrune_ForgetsVanishedProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	_ = fakeWorktree(t, "acme", "demo", "abc1234") // live worktree the lock points at
	proj := filepath.Join(t.TempDir(), "proj")
	_ = os.MkdirAll(proj, 0o755)
	livePath := recordProjectWithLock(t, proj)

	deadProj := filepath.Join(t.TempDir(), "dead")
	deadLock := filepath.Join(deadProj, model.LockFileName)
	// Record a project lock whose directory we then delete to simulate the
	// "user rm-rf'd the project" case.
	_ = os.MkdirAll(deadProj, 0o755)
	deadLockFile := model.NewLockFile(deadLock)
	if err := deadLockFile.Write(); err != nil {
		t.Fatalf("write dead lock: %v", err)
	}
	registry.TouchProject(deadLock)
	_ = os.RemoveAll(deadProj)

	resetPrinter(t)
	cachePruneDryRun = false
	if err := runCachePrune(nil, nil); err != nil {
		t.Fatalf("runCachePrune: %v", err)
	}

	pf, _ := registry.ReadProjects()
	if _, ok := pf.Projects[deadLock]; ok {
		t.Errorf("dead project should have been forgotten")
	}
	if _, ok := pf.Projects[livePath]; !ok {
		t.Errorf("live project should still be recorded")
	}
}
