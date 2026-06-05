package cmd

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/quiver-cli/qvr/internal/config"
	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/skill"
)

func resetSyncModeFlags(t *testing.T) {
	t.Helper()
	// cmd.Context() is nil when RunE is invoked directly (cobra sets it during
	// Execute); the scan path would panic without a real context.
	syncCmd.SetContext(context.Background())
	t.Cleanup(func() {
		syncGlobal = false
		syncDryRun = false
		syncLocked = false
		syncFrozen = false
		syncCheck = false
		syncAllowDrift = false
		syncCmd.SetContext(context.Background())
	})
	syncGlobal = false
	syncDryRun = false
	syncLocked = false
	syncFrozen = false
	syncCheck = false
	syncAllowDrift = false
}

// TestRunSync_ModesMutuallyExclusive guards the up-front rejection so a script
// can't pass two contradictory CI modes and silently get one's behaviour.
func TestRunSync_ModesMutuallyExclusive(t *testing.T) {
	resetSyncModeFlags(t)
	syncLocked = true
	syncCheck = true
	err := runSync(syncCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutual-exclusion error, got %v", err)
	}
}

// TestRunSync_CheckPassesWhenInSync: a freshly installed, untouched project is
// in sync — --check exits 0 and writes nothing.
func TestRunSync_CheckPassesWhenInSync(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Chdir(t.TempDir())
	resetSyncModeFlags(t)
	resetPrinter(t)
	installBranchPinned(t, "acme", "demo")

	project, _ := os.Getwd()
	lockPath := model.DefaultLockPath(project, config.Dir(), false)
	before, _ := os.ReadFile(lockPath)

	syncCheck = true
	if err := runSync(syncCmd, nil); err != nil {
		t.Fatalf("sync --check on in-sync project: %v", err)
	}
	after, _ := os.ReadFile(lockPath)
	if string(before) != string(after) {
		t.Errorf("--check mutated qvr.lock")
	}
}

// TestRunSync_CheckFailsWhenWorktreeMissing: a missing worktree means a real
// sync would restore it — --check must exit non-zero and restore nothing
// (read-only), leaving the worktree absent and the lock unchanged.
func TestRunSync_CheckFailsWhenWorktreeMissing(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Chdir(t.TempDir())
	resetSyncModeFlags(t)
	resetPrinter(t)
	installBranchPinned(t, "acme", "demo")

	project, _ := os.Getwd()
	lockPath := model.DefaultLockPath(project, config.Dir(), false)
	lock, _ := model.ReadLockFile(lockPath)
	entry, _ := lock.Get("demo")
	wt := skill.EntryWorktreePath(entry)
	if err := os.RemoveAll(wt); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}
	before, _ := os.ReadFile(lockPath)

	syncCheck = true
	err := runSync(syncCmd, nil)
	if err == nil {
		t.Fatal("sync --check with missing worktree returned nil; want non-zero")
	}
	// Read-only: worktree still absent, lock untouched.
	if _, statErr := os.Stat(wt); !os.IsNotExist(statErr) {
		t.Errorf("--check restored the worktree (stat err=%v); it must be read-only", statErr)
	}
	after, _ := os.ReadFile(lockPath)
	if string(before) != string(after) {
		t.Errorf("--check mutated qvr.lock")
	}
}

// TestRunSync_FrozenRestoresButFreezesLock: --frozen restores the worktree
// from the lock (so the project becomes usable) yet tolerates the stale state,
// exits 0, and leaves qvr.lock byte-identical.
func TestRunSync_FrozenRestoresButFreezesLock(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Chdir(t.TempDir())
	resetSyncModeFlags(t)
	resetPrinter(t)
	installBranchPinned(t, "acme", "demo")

	project, _ := os.Getwd()
	lockPath := model.DefaultLockPath(project, config.Dir(), false)
	lock, _ := model.ReadLockFile(lockPath)
	entry, _ := lock.Get("demo")
	wt := skill.EntryWorktreePath(entry)
	if err := os.RemoveAll(wt); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}
	before, _ := os.ReadFile(lockPath)

	syncFrozen = true
	if err := runSync(syncCmd, nil); err != nil {
		t.Fatalf("sync --frozen: %v", err)
	}
	// Worktree restored from the lock...
	if _, statErr := os.Stat(wt); statErr != nil {
		t.Errorf("--frozen should restore the worktree, stat err=%v", statErr)
	}
	// ...but the lock bytes are frozen.
	after, _ := os.ReadFile(lockPath)
	if string(before) != string(after) {
		t.Errorf("--frozen mutated qvr.lock:\nbefore=%s\nafter=%s", before, after)
	}
}
