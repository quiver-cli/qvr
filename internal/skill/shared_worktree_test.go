package skill_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quiver-cli/qvr/internal/git"
	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/registry"
	"github.com/quiver-cli/qvr/internal/skill"
)

// TestSharedWorktree_SwitchInOneProjectPreservesAnother is the regression
// test for bug #52. Two projects pin the same skill at the same ref. When
// Project B forces a reinstall onto a different ref (simulating
// `qvr switch` / `qvr upgrade`), Project A's worktree must not be touched
// — its lock entry, on-disk worktree, and symlink target all stay valid.
//
// Before the SHA-keyed worktree pivot, switch/upgrade in B would rename
// (or RemoveAll) the shared worktree, silently breaking A.
func TestSharedWorktree_SwitchInOneProjectPreservesAnother(t *testing.T) {
	home := testEnv(t)
	gc := git.NewGoGitClient()
	wt := git.NewGoGitWorktree()
	mgr := registry.NewManager(gc)
	inst := skill.NewInstaller(mgr, wt, gc)

	remote := seedRemoteWithTags(t, map[string]string{
		"code-review": codeReviewSkill,
	}, "v1.0.0", "v2.0.0")
	if _, err := mgr.Add(context.Background(), "acme", remote); err != nil {
		t.Fatalf("registry add: %v", err)
	}

	projectA := filepath.Join(home, "projA")
	projectB := filepath.Join(home, "projB")
	for _, p := range []string{projectA, projectB} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}

	// Project A pins v1.0.0.
	resultA, err := inst.Install(skill.InstallRequest{
		Skill:       "code-review@v1.0.0",
		Targets:     []string{"claude"},
		ProjectRoot: projectA,
	})
	if err != nil {
		t.Fatalf("A install: %v", err)
	}
	if _, err := os.Stat(resultA.Worktree); err != nil {
		t.Fatalf("A worktree should exist: %v", err)
	}

	// Project B pins the same ref — should share the same SHA-keyed worktree.
	resultB, err := inst.Install(skill.InstallRequest{
		Skill:       "code-review@v1.0.0",
		Targets:     []string{"claude"},
		ProjectRoot: projectB,
	})
	if err != nil {
		t.Fatalf("B install: %v", err)
	}
	if resultA.Worktree != resultB.Worktree {
		t.Errorf("shared SHA should share worktree: A=%s B=%s", resultA.Worktree, resultB.Worktree)
	}

	// Project B switches off to v2.0.0 via the install path that switch/upgrade
	// now use under the hood (Force=true install at the new ref).
	if _, err := inst.Install(skill.InstallRequest{
		Skill:       "code-review@v2.0.0",
		Targets:     []string{"claude"},
		ProjectRoot: projectB,
		Force:       true,
	}); err != nil {
		t.Fatalf("B upgrade: %v", err)
	}

	// Project A's worktree must still exist on disk and remain readable.
	if _, err := os.Stat(resultA.Worktree); err != nil {
		t.Fatalf("A worktree was GC'd by B's switch (bug #52): %v", err)
	}
	if _, err := os.Stat(filepath.Join(resultA.Worktree, "skills", "code-review", "SKILL.md")); err != nil {
		t.Fatalf("A worktree skill file disappeared: %v", err)
	}

	// A's lockfile entry still points where the file actually is.
	lockA, err := model.ReadLockFile(filepath.Join(projectA, model.LockFileName))
	if err != nil {
		t.Fatalf("read A lock: %v", err)
	}
	entryA, err := lockA.Get("code-review")
	if err != nil {
		t.Fatalf("A lock get: %v", err)
	}
	if skill.EntryWorktreePath(entryA) != resultA.Worktree {
		t.Errorf("A lock.Worktree changed: was %s, now %s", resultA.Worktree, skill.EntryWorktreePath(entryA))
	}

	// A's symlink target still resolves to A's worktree (not B's new one).
	linkA := filepath.Join(projectA, ".claude/skills/code-review")
	target, err := os.Readlink(linkA)
	if err != nil {
		t.Fatalf("readlink A: %v", err)
	}
	resolved, _ := filepath.EvalSymlinks(linkA)
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(linkA), target)
	}
	if !strings.HasPrefix(resolved, resultA.Worktree) && !strings.HasPrefix(target, resultA.Worktree) {
		t.Errorf("A symlink no longer points into A's worktree: target=%s resolved=%s want under %s",
			target, resolved, resultA.Worktree)
	}
}
