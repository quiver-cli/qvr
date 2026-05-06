package skilltests

import (
	"context"
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
	lock, err := model.ReadLockFile(filepath.Join(h.project, "qvr.lock.json"))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get("code-review")
	if err != nil {
		t.Fatalf("lock get: %v", err)
	}

	// 2. Modify via symlink (simulates an agent editing the skill).
	linkPath := filepath.Join(h.project, ".claude/skills/code-review", "SKILL.md")
	original, err := os.ReadFile(linkPath)
	if err != nil {
		t.Fatalf("read via symlink: %v", err)
	}
	newContent := append(original, []byte("\n## Added section\n")...)
	if err := os.WriteFile(linkPath, newContent, 0o644); err != nil {
		t.Fatalf("modify via symlink: %v", err)
	}

	// 3. Push — must pick up the edit and send to origin.
	syncer := skill.NewSyncer(git.NewGoGitWorktree(), git.NewGoGitClient())
	hash, err := syncer.Push(context.Background(), entry, skill.PushOptions{
		Message: "end-to-end edit",
		Author:  "Test",
	})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
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

	// 5. Switch to v2
	result, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review@v2",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	})
	if err != nil {
		t.Fatalf("install @v2: %v", err)
	}
	expected := registry.WorktreePath("acme", "code-review", "v2")
	if result.Worktree != expected {
		t.Errorf("v2 worktree = %s, want %s", result.Worktree, expected)
	}

	// 6. Remove — everything is cleaned up.
	if err := h.installer.Remove("code-review", skill.InstallRequest{ProjectRoot: h.project}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(h.project, ".claude/skills/code-review")); !os.IsNotExist(err) {
		t.Errorf("symlink survived remove: %v", err)
	}
}
