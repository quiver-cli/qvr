package skill_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/registry"
	"github.com/raks097/quiver/internal/skill"
)

// TestEndToEnd exercises the full install/edit/push round-trip: install,
// modify, push, pull (via a second "user"), switch versions, and remove.
// If any single step regresses, this test fails with a precise hint
// about where the break is.
func TestEndToEnd(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{
		"code-review": codeReviewSkill,
	}, "v2")
	h.addRegistry(t, "acme", remote)

	// 1. Install @main
	if _, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	lock, err := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get("code-review")
	if err != nil {
		t.Fatalf("lock get: %v", err)
	}

	// 2. Modify via symlink (simulates an agent editing the skill). The install
	// is frozen read-only; unlock it first, mirroring what `qvr edit` does.
	makeWorktreeEditable(t, skill.EntryWorktreePath(entry))
	linkPath := filepath.Join(h.project, ".claude/skills/code-review", "SKILL.md")
	original, err := os.ReadFile(linkPath)
	if err != nil {
		t.Fatalf("read via symlink: %v", err)
	}
	newContent := append(original, []byte("\n## Added section\n")...)
	if err := os.WriteFile(linkPath, newContent, 0o644); err != nil {
		t.Fatalf("modify via symlink: %v", err)
	}

	// 3. Commit + push the edit to origin. Production `qvr publish` pushes via
	//    git.Push directly; the helper mirrors that to seed the upstream commit.
	hash := commitAndPushWorktree(t, skill.EntryWorktreePath(entry), entry.Ref, "end-to-end edit")
	if len(hash) != 40 {
		t.Fatalf("push hash: %q", hash)
	}
	entry.Commit = hash
	lock.Put(entry)
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	// 4. Pull from a fresh second worktree to verify the push landed.
	second := filepath.Join(t.TempDir(), "second-install")
	wt := git.NewGoGitWorktree()
	if err := wt.Add(remote, second, "main"); err != nil {
		t.Fatalf("add second worktree: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(second, "skills", "code-review", "SKILL.md")); err != nil {
		t.Fatalf("read second: %v", err)
	} else if !strings.Contains(string(data), "Added section") {
		t.Errorf("push did not reach upstream")
	}

	// 5. Switch to v2 via Force install (the normal user path would be
	// `qvr switch`; here we exercise that Install accepts an explicit override).
	result, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review@v2",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
		Force:       true,
	})
	if err != nil {
		t.Fatalf("install @v2: %v", err)
	}
	// Worktree path is now SHA-keyed, so we can't compute it without
	// resolving v2 → SHA. Sanity-check it sits under the registry's
	// worktree tree and points at a real directory.
	if !strings.HasPrefix(result.Worktree, filepath.Join(registry.WorktreesRoot(), "acme", "code-review")+string(filepath.Separator)) {
		t.Errorf("v2 worktree %s not under expected registry/skill prefix", result.Worktree)
	}
	if _, err := os.Stat(result.Worktree); err != nil {
		t.Errorf("v2 worktree missing on disk: %v", err)
	}

	// 6. Remove — everything is cleaned up.
	if err := h.installer.Remove("code-review", skill.InstallRequest{ProjectRoot: h.project}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(h.project, ".claude/skills/code-review")); !os.IsNotExist(err) {
		t.Errorf("symlink survived remove: %v", err)
	}
}

// TestReconcile_RematerializesFromCache pins the recovery story from the
// plan: wipe ~/.quiver/worktrees/ behind a project's back, run `qvr sync`,
// and the reconciler must rebuild every missing worktree from the lock and
// re-create the agent symlinks. The lock — not the cache — is the
// authoritative source of truth.
func TestReconcile_RematerializesFromCache(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{
		"code-review": codeReviewSkill,
	})
	h.addRegistry(t, "acme", remote)

	if _, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	lockPath := filepath.Join(h.project, model.LockFileName)
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get("code-review")
	if err != nil {
		t.Fatalf("lock get: %v", err)
	}
	linkPath := filepath.Join(h.project, ".claude/skills/code-review")

	// Nuke the worktree behind the lock's back — the symlink is now
	// dangling. This simulates "user blew away ~/.quiver/worktrees/".
	if err := os.RemoveAll(skill.EntryWorktreePath(entry)); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(linkPath, "SKILL.md")); err == nil {
		t.Fatal("expected symlink to be dangling after worktree removal")
	}

	// qvr sync should re-create the worktree from the registry and
	// re-point the symlink at it.
	rec := skill.NewReconciler(h.installer)
	res, err := rec.Reconcile(lock, h.project, h.home, skill.ReconcileOptions{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(res.Installed) == 0 || res.Installed[0] != "code-review" {
		t.Errorf("expected code-review re-installed, got installed=%+v errors=%+v", res.Installed, res.Errors)
	}
	if _, err := os.Stat(filepath.Join(linkPath, "SKILL.md")); err != nil {
		t.Errorf("symlink should resolve to a real SKILL.md after sync, got %v", err)
	}
}
