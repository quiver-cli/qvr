package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/quiver-cli/qvr/internal/git"
	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/skill"
)

// TestSyncerStatus_EditModeNotBroken is the #117 regression for the
// status surface. Pre-fix, an edit-mode entry whose EditPath dir has
// no .git/ (the `qvr init` scaffold flow) was opened via
// gogit.PlainOpen → ENOENT → state=broken with `worktree unreadable:
// repository does not exist`. The fix special-cases edit-mode entries:
// directory exists + no git history is the expected scaffold state, so
// we report `edit` instead of `broken`.
func TestSyncerStatus_EditModeNotBroken(t *testing.T) {
	project := t.TempDir()
	editRel := filepath.Join(".claude", "skills", "demo")
	editAbs := filepath.Join(project, editRel)
	if err := os.MkdirAll(editAbs, 0o755); err != nil {
		t.Fatalf("mkdir edit dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(editAbs, "SKILL.md"),
		[]byte("---\nname: demo\ndescription: status edit-mode\n---\n# demo\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	entry := &model.LockEntry{
		Name:     "demo",
		Mode:     model.ModeEdit,
		EditPath: editRel,
		Ref:      "main",
		Targets:  []string{"claude"},
	}
	syncer := skill.NewSyncer(git.NewGoGitWorktree(), git.NewGoGitClient())
	s, err := syncer.Status(entry, project)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if s.Broken {
		t.Errorf("edit-mode entry without .git/ reported as broken (#117 regression): %+v", s)
	}
	if s.Message != "edit" {
		t.Errorf("Status.Message = %q, want 'edit' for scaffolded edit-mode entry", s.Message)
	}
}
