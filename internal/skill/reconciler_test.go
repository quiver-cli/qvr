package skill_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/registry"
	"github.com/raks097/quiver/internal/skill"
)

// newReconcilerHarness sets up an isolated quiver home + project so the
// reconciler's strict-remove pass operates against a known surface. The
// installer is intentionally nil here — these tests target the orphan
// detection logic, not the restore-from-lock path, which is exercised
// separately via integration_test.go.
type reconcilerHarness struct {
	project    string
	quiverHome string
	worktrees  string
}

func newReconcilerHarness(t *testing.T) *reconcilerHarness {
	t.Helper()
	h := &reconcilerHarness{
		project:    t.TempDir(),
		quiverHome: t.TempDir(),
	}
	t.Setenv("QUIVER_HOME", h.quiverHome)
	// config.Dir() reads QUIVER_HOME on every call — no cache to reset.
	h.worktrees = registry.WorktreesRoot()
	if err := os.MkdirAll(h.worktrees, 0o755); err != nil {
		t.Fatalf("mkdir worktrees: %v", err)
	}
	return h
}

// seedWorktree creates a fake worktree dir with a SKILL.md so CreateSymlink
// is happy when reconciler.fixSymlinks runs.
func (h *reconcilerHarness) seedWorktree(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(h.worktrees, "acme--"+name+"--abc1234")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	body := "---\nname: " + name + "\ndescription: test\n---\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	return dir
}

// TestReconcile_RemovesManagedOrphan pins the "hidden by default" guarantee:
// a symlink pointing into ~/.quiver/worktrees/ but with no lock entry is
// removed on sync.
func TestReconcile_RemovesManagedOrphan(t *testing.T) {
	h := newReconcilerHarness(t)
	orphanWT := h.seedWorktree(t, "orphan")

	claudeDir := filepath.Join(h.project, ".claude", "skills")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir claude: %v", err)
	}
	orphanLink := filepath.Join(claudeDir, "orphan")
	if err := os.Symlink(orphanWT, orphanLink); err != nil {
		t.Fatalf("seed orphan symlink: %v", err)
	}

	lockPath := filepath.Join(h.project, model.LockFileName)
	lock := model.NewLockFile(lockPath)
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	rec := skill.NewReconciler(nil)
	res, err := rec.Reconcile(lock, h.project, h.quiverHome, skill.ReconcileOptions{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(res.Removed) != 1 || res.Removed[0] != filepath.Clean(orphanLink) {
		t.Errorf("expected one removed orphan at %s, got %+v", orphanLink, res.Removed)
	}
	if _, err := os.Lstat(orphanLink); !os.IsNotExist(err) {
		t.Errorf("orphan symlink should be gone, stat err = %v", err)
	}
}

// TestReconcile_LeavesUnmanagedAlone is the key safety property: a symlink
// pointing OUTSIDE ~/.quiver/worktrees/ (e.g. into /etc/passwd) must never
// be touched, even if it has no lock entry.
func TestReconcile_LeavesUnmanagedAlone(t *testing.T) {
	h := newReconcilerHarness(t)

	// Use a tempdir as the "outside" path so we don't actually point at
	// /etc/passwd — but the categorization logic is identical.
	outside := t.TempDir()
	body := "---\nname: outside\ndescription: x\n---\n#outside\n"
	if err := os.WriteFile(filepath.Join(outside, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("seed outside skill: %v", err)
	}

	claudeDir := filepath.Join(h.project, ".claude", "skills")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir claude: %v", err)
	}
	outsideLink := filepath.Join(claudeDir, "outside")
	if err := os.Symlink(outside, outsideLink); err != nil {
		t.Fatalf("seed outside symlink: %v", err)
	}

	lockPath := filepath.Join(h.project, model.LockFileName)
	lock := model.NewLockFile(lockPath)
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	rec := skill.NewReconciler(nil)
	res, err := rec.Reconcile(lock, h.project, h.quiverHome, skill.ReconcileOptions{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(res.Removed) != 0 {
		t.Errorf("unmanaged symlink must not be removed, got removed=%+v", res.Removed)
	}
	if _, err := os.Lstat(outsideLink); err != nil {
		t.Errorf("unmanaged symlink should remain, stat err = %v", err)
	}
	if len(res.Skipped) == 0 {
		t.Errorf("expected the unmanaged symlink to be reported in Skipped, got %+v", res.Skipped)
	}
}

// TestReconcile_LeavesPlainDirsAlone confirms that a hand-placed directory
// (not a symlink) under .claude/skills/ is never touched, even when the lock
// is empty.
func TestReconcile_LeavesPlainDirsAlone(t *testing.T) {
	h := newReconcilerHarness(t)

	claudeDir := filepath.Join(h.project, ".claude", "skills", "sneaky")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir sneaky: %v", err)
	}
	skillMD := filepath.Join(claudeDir, "SKILL.md")
	if err := os.WriteFile(skillMD, []byte("---\nname: sneaky\ndescription: hand-placed\n---\n# x\n"), 0o644); err != nil {
		t.Fatalf("seed SKILL.md: %v", err)
	}

	lockPath := filepath.Join(h.project, model.LockFileName)
	lock := model.NewLockFile(lockPath)
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	rec := skill.NewReconciler(nil)
	res, err := rec.Reconcile(lock, h.project, h.quiverHome, skill.ReconcileOptions{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(res.Removed) != 0 {
		t.Errorf("plain directory must never be removed, got %+v", res.Removed)
	}
	if _, err := os.Stat(skillMD); err != nil {
		t.Errorf("plain SKILL.md should remain, stat err = %v", err)
	}
}

// TestReconcile_KeepUntrackedDowngradesToWarning verifies the escape hatch:
// when the user opts out, orphans surface in Skipped instead of getting
// removed.
func TestReconcile_KeepUntrackedDowngradesToWarning(t *testing.T) {
	h := newReconcilerHarness(t)
	orphanWT := h.seedWorktree(t, "orphan")

	claudeDir := filepath.Join(h.project, ".claude", "skills")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir claude: %v", err)
	}
	orphanLink := filepath.Join(claudeDir, "orphan")
	if err := os.Symlink(orphanWT, orphanLink); err != nil {
		t.Fatalf("seed orphan symlink: %v", err)
	}

	lockPath := filepath.Join(h.project, model.LockFileName)
	lock := model.NewLockFile(lockPath)
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	rec := skill.NewReconciler(nil)
	res, err := rec.Reconcile(lock, h.project, h.quiverHome, skill.ReconcileOptions{KeepUntracked: true})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(res.Removed) != 0 {
		t.Errorf("--keep-untracked should preserve orphan, got removed=%+v", res.Removed)
	}
	if _, err := os.Lstat(orphanLink); err != nil {
		t.Errorf("orphan must survive --keep-untracked, stat err = %v", err)
	}
	if len(res.Skipped) == 0 {
		t.Errorf("expected orphan reported in Skipped under --keep-untracked")
	}
}

// TestReconcile_DryRunDoesNotMutate confirms --dry-run reports the would-be
// removal without touching disk.
func TestReconcile_DryRunDoesNotMutate(t *testing.T) {
	h := newReconcilerHarness(t)
	orphanWT := h.seedWorktree(t, "orphan")

	claudeDir := filepath.Join(h.project, ".claude", "skills")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir claude: %v", err)
	}
	orphanLink := filepath.Join(claudeDir, "orphan")
	if err := os.Symlink(orphanWT, orphanLink); err != nil {
		t.Fatalf("seed orphan symlink: %v", err)
	}

	lockPath := filepath.Join(h.project, model.LockFileName)
	lock := model.NewLockFile(lockPath)
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	rec := skill.NewReconciler(nil)
	res, err := rec.Reconcile(lock, h.project, h.quiverHome, skill.ReconcileOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, err := os.Lstat(orphanLink); err != nil {
		t.Errorf("--dry-run must not touch the symlink, stat err = %v", err)
	}
	if len(res.Removed) != 1 || !strings.Contains(res.Removed[0], "would remove") {
		t.Errorf("expected dry-run marker on the would-be removal, got %+v", res.Removed)
	}
}

// TestReconcile_IgnoresRegularFile is the file-flavored sibling of
// TestReconcile_LeavesPlainDirsAlone: a hand-placed regular file under
// .claude/skills/ (e.g. someone copied a SKILL.md directly) must never
// be removed. The strict pass only acts on symlinks.
func TestReconcile_IgnoresRegularFile(t *testing.T) {
	h := newReconcilerHarness(t)

	claudeDir := filepath.Join(h.project, ".claude", "skills")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir claude: %v", err)
	}
	regular := filepath.Join(claudeDir, "manual.md")
	if err := os.WriteFile(regular, []byte("hand-written note"), 0o644); err != nil {
		t.Fatalf("seed regular file: %v", err)
	}

	lockPath := filepath.Join(h.project, model.LockFileName)
	lock := model.NewLockFile(lockPath)
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	rec := skill.NewReconciler(nil)
	res, err := rec.Reconcile(lock, h.project, h.quiverHome, skill.ReconcileOptions{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(res.Removed) != 0 {
		t.Errorf("regular file must never be removed, got %+v", res.Removed)
	}
	if _, err := os.Stat(regular); err != nil {
		t.Errorf("regular file should remain, stat err = %v", err)
	}
}

// TestReconcile_RemovesDanglingManagedSymlink pins the recovery case:
// a symlink under .claude/skills/ pointing at a path that *would* be
// inside ~/.quiver/worktrees/ but the target dir has been deleted (cache
// cleared, machine wiped, etc.). It's still a qvr-managed orphan — the
// reconciler should remove it cleanly. The `isManaged` allowlist works on
// the symlink's textual target, not the resolved one, so a missing target
// doesn't change the classification.
func TestReconcile_RemovesDanglingManagedSymlink(t *testing.T) {
	h := newReconcilerHarness(t)

	// Point at a path under worktrees/ that doesn't actually exist.
	ghostTarget := filepath.Join(h.worktrees, "acme--gone--abc1234")

	claudeDir := filepath.Join(h.project, ".claude", "skills")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir claude: %v", err)
	}
	dangling := filepath.Join(claudeDir, "ghost")
	if err := os.Symlink(ghostTarget, dangling); err != nil {
		t.Fatalf("seed dangling symlink: %v", err)
	}

	lockPath := filepath.Join(h.project, model.LockFileName)
	lock := model.NewLockFile(lockPath)
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	rec := skill.NewReconciler(nil)
	res, err := rec.Reconcile(lock, h.project, h.quiverHome, skill.ReconcileOptions{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(res.Removed) != 1 || res.Removed[0] != filepath.Clean(dangling) {
		t.Errorf("expected dangling managed symlink removed, got %+v", res.Removed)
	}
	if _, err := os.Lstat(dangling); !os.IsNotExist(err) {
		t.Errorf("dangling symlink should be gone, stat err = %v", err)
	}
}

// TestReconcile_KeepsTrackedSymlink confirms that a symlink corresponding to
// a lock entry is left alone (not removed, not reported as orphan).
func TestReconcile_KeepsTrackedSymlink(t *testing.T) {
	h := newReconcilerHarness(t)
	trackedWT := h.seedWorktree(t, "tdd")

	claudeDir := filepath.Join(h.project, ".claude", "skills")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir claude: %v", err)
	}
	link := filepath.Join(claudeDir, "tdd")
	if err := os.Symlink(trackedWT, link); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}

	lockPath := filepath.Join(h.project, model.LockFileName)
	lock := model.NewLockFile(lockPath)
	lock.Put(&model.LockEntry{
		Name:     "tdd",
		Registry: "acme",
		Ref:      "main",
		Commit:   "abc1234",
		Targets:  []string{"claude"},
		Source:   "registry",
	})
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	rec := skill.NewReconciler(nil)
	res, err := rec.Reconcile(lock, h.project, h.quiverHome, skill.ReconcileOptions{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(res.Removed) != 0 {
		t.Errorf("tracked symlink must not be removed, got %+v", res.Removed)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("tracked symlink must remain, stat err = %v", err)
	}
}
