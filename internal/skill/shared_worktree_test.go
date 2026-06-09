package skill_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astra-sh/qvr/internal/git"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/registry"
	"github.com/astra-sh/qvr/internal/skill"
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
	resultA := installSharedAt(t, inst, "code-review@v1.0.0", projectA, false)
	if _, err := os.Stat(resultA.Worktree); err != nil {
		t.Fatalf("A worktree should exist: %v", err)
	}

	// Project B pins the same ref — should share the same SHA-keyed worktree.
	resultB := installSharedAt(t, inst, "code-review@v1.0.0", projectB, false)
	if resultA.Worktree != resultB.Worktree {
		t.Errorf("shared SHA should share worktree: A=%s B=%s", resultA.Worktree, resultB.Worktree)
	}

	// Project B switches off to v2.0.0 via the install path that switch/upgrade
	// now use under the hood (Force=true install at the new ref).
	installSharedAt(t, inst, "code-review@v2.0.0", projectB, true)

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
	assertSymlinkUnder(t, linkA, resultA.Worktree)
}

// installSharedAt installs ref into projectRoot for the claude target (forced
// when force is set) and returns the install result, failing the test on error.
func installSharedAt(t *testing.T, inst *skill.Installer, ref, projectRoot string, force bool) *skill.InstallResult {
	t.Helper()
	res, err := inst.Install(skill.InstallRequest{
		Skill:       ref,
		Targets:     []string{"claude"},
		ProjectRoot: projectRoot,
		Force:       force,
	})
	if err != nil {
		t.Fatalf("install %s into %s: %v", ref, projectRoot, err)
	}
	return res
}

// assertSymlinkUnder fails if linkPath doesn't resolve to (or point at, via its
// raw target) a path under wantPrefix.
func assertSymlinkUnder(t *testing.T, linkPath, wantPrefix string) {
	t.Helper()
	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink %s: %v", linkPath, err)
	}
	resolved, _ := filepath.EvalSymlinks(linkPath)
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(linkPath), target)
	}
	if !strings.HasPrefix(resolved, wantPrefix) && !strings.HasPrefix(target, wantPrefix) {
		t.Errorf("symlink no longer points under expected worktree: target=%s resolved=%s want under %s",
			target, resolved, wantPrefix)
	}
}
