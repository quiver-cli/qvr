package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/quiver-cli/qvr/internal/config"
	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/registry"
	"github.com/quiver-cli/qvr/internal/skill"
)

func TestDisableSkill_RemovesSymlinks(t *testing.T) {
	src := writeFullSkill(t, "demo")
	project := t.TempDir()
	linkSkillInto(t, project, ".claude/skills", "demo", src)
	linkSkillInto(t, project, ".cursor/rules", "demo", src)

	entry := &model.LockEntry{
		Name:    "demo",
		Source:  src,
		Ref:     "local",
		Targets: []string{"claude", "cursor"},
	}
	removed, err := disableSkill(entry, project, false)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if len(removed) != 2 {
		t.Errorf("want 2 removed symlinks, got %d", len(removed))
	}
	for _, rel := range []string{".claude/skills/demo", ".cursor/rules/demo"} {
		if _, err := os.Lstat(filepath.Join(project, rel)); !os.IsNotExist(err) {
			t.Errorf("symlink %s should be gone, got err=%v", rel, err)
		}
	}
}

func TestDisableSkill_Idempotent(t *testing.T) {
	src := writeFullSkill(t, "demo")
	project := t.TempDir()
	linkSkillInto(t, project, ".claude/skills", "demo", src)

	entry := &model.LockEntry{
		Name:    "demo",
		Source:  src,
		Ref:     "local",
		Targets: []string{"claude"},
	}
	if _, err := disableSkill(entry, project, false); err != nil {
		t.Fatalf("first disable: %v", err)
	}
	removed, err := disableSkill(entry, project, false)
	if err != nil {
		t.Fatalf("second disable should not error: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("re-disable should remove nothing, got %d", len(removed))
	}
}

func TestEnableSkill_RestoresSymlinks(t *testing.T) {
	src := writeFullSkill(t, "demo")
	project := t.TempDir()

	entry := &model.LockEntry{
		Name:    "demo",
		Source:  src,
		Ref:     "local",
		Targets: []string{"claude", "cursor"},
	}
	created, err := enableSkill(entry, project, false)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if len(created) != 2 {
		t.Errorf("want 2 created symlinks, got %d", len(created))
	}
	for _, rel := range []string{".claude/skills/demo", ".cursor/rules/demo"} {
		full := filepath.Join(project, rel)
		if err := skill.VerifyTarget(full, src); err != nil {
			t.Errorf("symlink %s should point at %s: %v", full, src, err)
		}
	}
}

func TestEnableSkill_Idempotent(t *testing.T) {
	src := writeFullSkill(t, "demo")
	project := t.TempDir()

	entry := &model.LockEntry{
		Name:    "demo",
		Source:  src,
		Ref:     "local",
		Targets: []string{"claude"},
	}
	if _, err := enableSkill(entry, project, false); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := enableSkill(entry, project, false); err != nil {
		t.Errorf("second enable should be a no-op: %v", err)
	}
}

func TestEnableSkill_NoWorktree(t *testing.T) {
	entry := &model.LockEntry{Name: "demo", Targets: []string{"claude"}}
	_, err := enableSkill(entry, t.TempDir(), false)
	if err == nil {
		t.Fatal("expected error when worktree is empty")
	}
}

// TestEnableSkill_HonorsSkillPath pins the bug #12 fix: when the skill's
// SKILL.md lives at `<worktree>/<Path>/SKILL.md` (the standard registry
// layout), enable must link at the leaf, not the worktree root. Pre-fix,
// enableSkill passed entry.Worktree to CreateSymlink which rejected it
// because the root has no SKILL.md.
func TestEnableSkill_HonorsSkillPath(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	reg, name, commit := "r", "demo", "abc1234"
	worktree := registry.WorktreePath(reg, name, registry.ShortSHA(commit))
	skillDir := filepath.Join(worktree, "skills", "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir leaf: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: demo\ndescription: x\n---\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	project := t.TempDir()

	entry := &model.LockEntry{
		Name:     "demo",
		Registry: reg,
		Source:   "git@example.test:" + reg + ".git",
		Path:     "skills/demo",
		Ref:      "main",
		Commit:   commit,
		Targets:  []string{"claude"},
	}
	created, err := enableSkill(entry, project, false)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("want 1 created link, got %d", len(created))
	}
	if err := skill.VerifyTarget(filepath.Join(project, ".claude/skills/demo"), skillDir); err != nil {
		t.Errorf("symlink should point at leaf (%s): %v", skillDir, err)
	}
}

func TestDisableEnableRoundTrip(t *testing.T) {
	src := writeFullSkill(t, "demo")
	project := t.TempDir()
	linkSkillInto(t, project, ".claude/skills", "demo", src)

	entry := &model.LockEntry{
		Name:    "demo",
		Source:  src,
		Ref:     "local",
		Targets: []string{"claude"},
	}

	if _, err := disableSkill(entry, project, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(project, ".claude/skills/demo")); !os.IsNotExist(err) {
		t.Errorf("symlink should be gone after disable")
	}

	if _, err := enableSkill(entry, project, false); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := skill.VerifyTarget(filepath.Join(project, ".claude/skills/demo"), src); err != nil {
		t.Errorf("symlink should be restored after enable: %v", err)
	}
}

func TestDoctor_DisabledSymlinkSkipped(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	_ = writeFullSkill(t, "demo")
	project := t.TempDir()

	lock := model.NewLockFile(filepath.Join(project, model.LockFileName))
	lock.Put(&model.LockEntry{
		Name:     "demo",
		Targets:  []string{"claude"},
		Disabled: true,
	})
	checks := runDoctorChecks(lock, config.Default(), project)
	sym := findCheck(t, checks, "symlink", "demo")
	if !sym.OK {
		t.Errorf("disabled skill should pass symlink check (no symlink expected): %+v", sym)
	}
}

// confirm RemoveSymlink not-found error type still matches what disableSkill expects.
func TestDisable_RecognisesNotFoundSentinel(t *testing.T) {
	wrapped := fmt.Errorf("remove symlink /tmp/foo: %w", skill.ErrSymlinkNotFound)
	if !errors.Is(wrapped, skill.ErrSymlinkNotFound) {
		t.Fatalf("errors.Is did not unwrap ErrSymlinkNotFound from %v", wrapped)
	}
}
