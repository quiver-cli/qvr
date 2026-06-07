package skill_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/skill"
)

// TestInstaller_ConcurrentInstallSerialisesUnderWithLock reproduces the
// scenario from bug #55: three concurrent qvr add calls in the same project,
// each adding a different skill. Without flock, last-writer-wins and only
// one entry survives in qvr.lock. With model.WithLock around each install,
// all three must land.
func TestInstaller_ConcurrentInstallSerialisesUnderWithLock(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{
		"alpha": testSkill("alpha"),
		"bravo": testSkill("bravo"),
		"delta": testSkill("delta"),
	})
	h.addRegistry(t, "acme", remote)

	lockPath := filepath.Join(h.project, model.LockFileName)
	quiverHome := t.TempDir()
	names := []string{"alpha", "bravo", "delta"}

	var wg sync.WaitGroup
	wg.Add(len(names))
	errs := make(chan error, len(names))
	for _, name := range names {
		name := name // capture loop variable
		go func() {
			defer wg.Done()
			err := model.WithLock(quiverHome, lockPath, func() error {
				_, ierr := h.installer.Install(skill.InstallRequest{
					Skill:       name,
					Targets:     []string{"claude"},
					ProjectRoot: h.project,
					LockPath:    lockPath,
				})
				return ierr
			})
			if err != nil {
				errs <- fmt.Errorf("install %s: %w", name, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("%v", err)
	}

	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	if got := len(lock.Skills); got != len(names) {
		present := make([]string, 0, len(lock.Skills))
		for n := range lock.Skills {
			present = append(present, n)
		}
		t.Fatalf("expected %d entries after concurrent installs, got %d: %v", len(names), got, present)
	}
	for _, name := range names {
		if _, err := lock.Get(name); err != nil {
			t.Errorf("missing skill %q after concurrent install: %v", name, err)
		}
	}
}

func testSkill(name string) string {
	return fmt.Sprintf("---\nname: %s\ndescription: Concurrency test skill %s.\n---\n\n# %s\n",
		name, name, name)
}

// TestPrematerializeBatch_BuildsAllContentDirs verifies the #206 parallel
// pre-pass materializes every skill's content dir up front, so the subsequent
// serial Install reuses them. Asserts each dir exists, is worktree-free, and
// that a following Install records an agreeing SubtreeHash (i.e. the pre-built
// dir is a valid input to Install).
func TestPrematerializeBatch_BuildsAllContentDirs(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{
		"alpha": testSkill("alpha"),
		"bravo": testSkill("bravo"),
		"delta": testSkill("delta"),
	})
	h.addRegistry(t, "acme", remote)
	lockPath := filepath.Join(h.project, model.LockFileName)

	reqs := []skill.InstallRequest{
		{Skill: "alpha", Targets: []string{"claude"}, ProjectRoot: h.project, LockPath: lockPath},
		{Skill: "bravo", Targets: []string{"claude"}, ProjectRoot: h.project, LockPath: lockPath},
		{Skill: "delta", Targets: []string{"claude"}, ProjectRoot: h.project, LockPath: lockPath},
	}
	h.installer.PrematerializeBatch(reqs)

	for _, req := range reqs {
		if _, err := h.installer.Install(req); err != nil {
			t.Fatalf("install %s after prematerialize: %v", req.Skill, err)
		}
	}
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	for _, name := range []string{"alpha", "bravo", "delta"} {
		entry, err := lock.Get(name)
		if err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
		dir := skill.EntryWorktreePath(entry)
		if _, err := os.Stat(filepath.Join(dir, entry.Path, "SKILL.md")); err != nil {
			t.Errorf("%s content dir not materialized: %v", name, err)
		}
		if skill.HasGitDir(dir) {
			t.Errorf("%s content dir carries .git (want worktree-free)", name)
		}
		if res := skill.VerifySingleEntry(entry, h.project); res.Status != skill.VerifyStatusOK {
			t.Errorf("%s verify after prematerialize+install = %q (%s)", name, res.Status, res.Message)
		}
	}
}

// TestInstallInto_BatchSharedLock exercises the batch add path: read the lock
// ONCE, run every install against the shared in-memory lock, write ONCE. All
// entries must land and verify cleanly — the O(N) lockfile contract that
// replaced the old per-skill ReadLockFile+Write (the warm multi-skill cost).
func TestInstallInto_BatchSharedLock(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{
		"alpha": testSkill("alpha"),
		"bravo": testSkill("bravo"),
		"delta": testSkill("delta"),
	})
	h.addRegistry(t, "acme", remote)
	lockPath := filepath.Join(h.project, model.LockFileName)

	reqs := []skill.InstallRequest{
		{Skill: "alpha", Targets: []string{"claude"}, ProjectRoot: h.project, LockPath: lockPath},
		{Skill: "bravo", Targets: []string{"claude"}, ProjectRoot: h.project, LockPath: lockPath},
		{Skill: "delta", Targets: []string{"claude"}, ProjectRoot: h.project, LockPath: lockPath},
	}
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	for _, req := range reqs {
		if _, err := h.installer.InstallInto(req, lock); err != nil {
			t.Fatalf("InstallInto %s: %v", req.Skill, err)
		}
	}
	if err := lock.Write(); err != nil {
		t.Fatalf("single batch write: %v", err)
	}

	got, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("reread lock: %v", err)
	}
	for _, name := range []string{"alpha", "bravo", "delta"} {
		entry, err := got.Get(name)
		if err != nil {
			t.Fatalf("missing %s after single-write batch: %v", name, err)
		}
		if res := skill.VerifySingleEntry(entry, h.project); res.Status != skill.VerifyStatusOK {
			t.Errorf("%s verify = %q (%s)", name, res.Status, res.Message)
		}
	}
}

// TestRemoveFrom_InMemoryRollback covers the scan-block rollback in the batch
// path: a skill installed into the shared lock is rolled back in-memory (no
// extra lock read/write) before the single final write, leaving no lock entry
// and no symlink — exactly what `qvr add` does when the scan gate blocks one
// skill in a multi-skill batch.
func TestRemoveFrom_InMemoryRollback(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{
		"alpha": testSkill("alpha"),
		"bravo": testSkill("bravo"),
	})
	h.addRegistry(t, "acme", remote)
	lockPath := filepath.Join(h.project, model.LockFileName)

	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	base := skill.InstallRequest{Targets: []string{"claude"}, ProjectRoot: h.project, LockPath: lockPath}
	alpha := base
	alpha.Skill = "alpha"
	bravo := base
	bravo.Skill = "bravo"
	if _, err := h.installer.InstallInto(alpha, lock); err != nil {
		t.Fatalf("install alpha: %v", err)
	}
	res, err := h.installer.InstallInto(bravo, lock)
	if err != nil {
		t.Fatalf("install bravo: %v", err)
	}
	// Simulate a scan block on bravo: roll it back in-memory before the write.
	if err := h.installer.RemoveFrom(res.Name, base, lock); err != nil {
		t.Fatalf("RemoveFrom bravo: %v", err)
	}
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	got, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("reread lock: %v", err)
	}
	if _, err := got.Get("alpha"); err != nil {
		t.Errorf("alpha should survive the batch: %v", err)
	}
	if _, err := got.Get("bravo"); err == nil {
		t.Errorf("bravo should have been rolled out of the lock by RemoveFrom")
	}
	bravoLink := filepath.Join(h.project, ".claude", "skills", "bravo")
	if _, err := os.Lstat(bravoLink); !os.IsNotExist(err) {
		t.Errorf("bravo symlink should be torn down, got lstat err = %v", err)
	}
}

// TestPrematerializeBatch_SharedContentDirNoCorruption stresses the staging
// uniqueness: two requests for the SAME skill@SHA (here via two --as aliases)
// resolve to one shared content dir. The parallel pre-pass must not let their
// staging dirs collide and corrupt the result.
func TestPrematerializeBatch_SharedContentDirNoCorruption(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"alpha": testSkill("alpha")})
	h.addRegistry(t, "acme", remote)
	lockPath := filepath.Join(h.project, model.LockFileName)

	reqs := []skill.InstallRequest{
		{Skill: "alpha", As: "one", Targets: []string{"claude"}, ProjectRoot: h.project, LockPath: lockPath},
		{Skill: "alpha", As: "two", Targets: []string{"claude"}, ProjectRoot: h.project, LockPath: lockPath},
	}
	h.installer.PrematerializeBatch(reqs)

	for _, req := range reqs {
		if _, err := h.installer.Install(req); err != nil {
			t.Fatalf("install %s: %v", req.As, err)
		}
	}
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	// Both aliases share one canonical content dir; both must verify cleanly.
	for _, alias := range []string{"one", "two"} {
		entry, err := lock.Get(alias)
		if err != nil {
			t.Fatalf("missing alias %s: %v", alias, err)
		}
		if res := skill.VerifySingleEntry(entry, h.project); res.Status != skill.VerifyStatusOK {
			t.Errorf("alias %s verify = %q (%s)", alias, res.Status, res.Message)
		}
	}
}
