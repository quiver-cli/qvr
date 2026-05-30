package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/raks097/quiver/internal/model"
)

// TestNeedsLockWrite_ByteIdentical is the unit-level guard for issue #79's
// idempotency fix: when the in-memory lock would serialise to the exact
// bytes already on disk, sync must NOT call lock.Write. The helper makes
// that decision; this test pins it.
func TestNeedsLockWrite_ByteIdentical(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "qvr.lock")
	lock := model.NewLockFile(lockPath)
	lock.Put(&model.LockEntry{
		Name:          "demo",
		Source:        "https://example.com/repo.git",
		Ref:           "main",
		Commit:        "abc1234567890abcdef1234567890abcdef12345",
		InstallCommit: "abc1234567890abcdef1234567890abcdef12345",
		SubtreeHash:   "sha256:deadbeef",
		Targets:       []string{"claude"},
		InstalledAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	priorBytes, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read lock back: %v", err)
	}

	// Same in-memory state → should report "no write needed".
	if needsLockWrite(lock, priorBytes) {
		t.Errorf("needsLockWrite=true for unchanged lock — sync would churn (issue #79)")
	}

	// Mutate the in-memory lock; helper must now report "write needed".
	e, _ := lock.Get("demo")
	e.SubtreeHash = "sha256:newhash"
	if !needsLockWrite(lock, priorBytes) {
		t.Errorf("needsLockWrite=false after mutation — sync would skip a real change")
	}
}

// TestNeedsLockWrite_EmptyPriorAlwaysWrites covers the bootstrap path:
// when no qvr.lock exists yet (first sync ever), the helper must default
// to writing. Otherwise the fresh lockfile would never reach disk.
func TestNeedsLockWrite_EmptyPriorAlwaysWrites(t *testing.T) {
	lock := model.NewLockFile(filepath.Join(t.TempDir(), "qvr.lock"))
	if !needsLockWrite(lock, nil) {
		t.Errorf("needsLockWrite=false for empty prior — first sync would skip the write")
	}
	if !needsLockWrite(lock, []byte{}) {
		t.Errorf("needsLockWrite=false for zero-length prior")
	}
}
