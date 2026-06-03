package skill_test

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/skill"
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
