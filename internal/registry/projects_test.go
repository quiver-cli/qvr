package registry_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/registry"
)

func setHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	return home
}

func writeLock(t *testing.T, path string, entries ...*model.LockEntry) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	lock := model.NewLockFile(path)
	for _, e := range entries {
		lock.Put(e)
	}
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}
}

func TestTouchProject_RecordsAndDeduplicates(t *testing.T) {
	setHome(t)

	lockA := filepath.Join(t.TempDir(), "qvr.lock")
	lockB := filepath.Join(t.TempDir(), "qvr.lock")

	registry.TouchProject(lockA)
	registry.TouchProject(lockB)
	registry.TouchProject(lockA) // dedupes

	pf, err := registry.ReadProjects()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(pf.Projects) != 2 {
		t.Fatalf("want 2 records, got %d (%v)", len(pf.Projects), pf.Projects)
	}
	if _, ok := pf.Projects[lockA]; !ok {
		t.Errorf("lockA missing from projects.json")
	}
	if _, ok := pf.Projects[lockB]; !ok {
		t.Errorf("lockB missing from projects.json")
	}
}

func TestTouchProject_SkipsGlobalLock(t *testing.T) {
	home := setHome(t)
	globalLock := filepath.Join(home, model.LockFileName)
	registry.TouchProject(globalLock)

	pf, err := registry.ReadProjects()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, ok := pf.Projects[globalLock]; ok {
		t.Errorf("global lock should not be recorded in projects.json")
	}
}

func TestReachable_CollectsAllProjectWorktrees(t *testing.T) {
	home := setHome(t)

	projA := filepath.Join(t.TempDir(), "projA")
	projB := filepath.Join(t.TempDir(), "projB")
	_ = home // worktree paths derived via WorktreePath which honours QUIVER_HOME
	wtA := registry.WorktreePath("acme", "tdd", registry.ShortSHA("abc1234abcdef"))
	wtB := registry.WorktreePath("acme", "tdd", registry.ShortSHA("def5678abcdef"))
	wtBOther := registry.WorktreePath("acme", "other", registry.ShortSHA("111aaa999abc"))

	writeLock(t, filepath.Join(projA, "qvr.lock"), &model.LockEntry{
		Name:     "tdd",
		Registry: "acme",
		Source:   "git@example:acme.git",
		Ref:      "main",
		Commit:   "abc1234abcdef",
	})
	writeLock(t, filepath.Join(projB, "qvr.lock"),
		&model.LockEntry{Name: "tdd", Registry: "acme", Source: "git@example:acme.git", Ref: "main", Commit: "def5678abcdef"},
		&model.LockEntry{Name: "other", Registry: "acme", Source: "git@example:acme.git", Ref: "main", Commit: "111aaa999abc"},
	)

	registry.TouchProject(filepath.Join(projA, "qvr.lock"))
	registry.TouchProject(filepath.Join(projB, "qvr.lock"))

	res, err := registry.Reachable()
	if err != nil {
		t.Fatalf("reachable: %v", err)
	}
	want := []string{wtA, wtB, wtBOther}
	for _, w := range want {
		if _, ok := res.Worktrees[w]; !ok {
			t.Errorf("missing %s from reachability set", w)
		}
	}
	if len(res.Worktrees) != 3 {
		t.Errorf("want 3 worktrees, got %d: %v", len(res.Worktrees), res.Worktrees)
	}
	if len(res.MissingProjects) != 0 {
		t.Errorf("want 0 missing projects, got %v", res.MissingProjects)
	}
}

func TestReachable_FlagsMissingProjectLock(t *testing.T) {
	setHome(t)

	live := filepath.Join(t.TempDir(), "qvr.lock")
	writeLock(t, live, &model.LockEntry{Name: "x", Registry: "r", Source: "git@example:r.git", Ref: "main", Commit: "abc1234"})
	expectedWt := registry.WorktreePath("r", "x", registry.ShortSHA("abc1234"))

	dead := filepath.Join(t.TempDir(), "vanished", "qvr.lock")
	// Don't write — file does not exist on disk.

	registry.TouchProject(live)
	registry.TouchProject(dead)

	res, err := registry.Reachable()
	if err != nil {
		t.Fatalf("reachable: %v", err)
	}
	if _, ok := res.Worktrees[expectedWt]; !ok {
		t.Errorf("live project's worktree missing from reachability (want %s, got %v)", expectedWt, res.Worktrees)
	}
	if len(res.MissingProjects) != 1 || res.MissingProjects[0] != dead {
		t.Errorf("expected MissingProjects=[%s], got %v", dead, res.MissingProjects)
	}
}

func TestReachable_AlwaysIncludesGlobalLock(t *testing.T) {
	home := setHome(t)

	globalLock := filepath.Join(home, model.LockFileName)
	writeLock(t, globalLock, &model.LockEntry{
		Name: "ambient", Registry: "r", Source: "git@example:r.git", Ref: "main", Commit: "abc1234",
	})
	expectedWt := registry.WorktreePath("r", "ambient", registry.ShortSHA("abc1234"))

	res, err := registry.Reachable()
	if err != nil {
		t.Fatalf("reachable: %v", err)
	}
	if _, ok := res.Worktrees[expectedWt]; !ok {
		t.Errorf("global lock's worktree missing from reachability set (want %s, got %v)", expectedWt, res.Worktrees)
	}
}

func TestForgetProject_RemovesRecord(t *testing.T) {
	setHome(t)

	a := filepath.Join(t.TempDir(), "qvr.lock")
	b := filepath.Join(t.TempDir(), "qvr.lock")
	registry.TouchProject(a)
	registry.TouchProject(b)
	registry.ForgetProject(a)

	pf, _ := registry.ReadProjects()
	if _, ok := pf.Projects[a]; ok {
		t.Errorf("expected a forgotten, still present")
	}
	if _, ok := pf.Projects[b]; !ok {
		t.Errorf("b was forgotten too")
	}
}
