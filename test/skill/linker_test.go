package skilltests

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/raks097/quiver/internal/skill"
)

func TestCreateSymlink(t *testing.T) {
	skillDir := makeSkillDir(t, "code-review")
	linkPath := filepath.Join(t.TempDir(), ".claude", "skills", "code-review")

	if err := skill.CreateSymlink(linkPath, skillDir); err != nil {
		t.Fatalf("CreateSymlink: %v", err)
	}

	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	abs, _ := filepath.Abs(skillDir)
	if target != abs {
		t.Errorf("target = %q, want %q", target, abs)
	}
}

func TestCreateSymlink_Idempotent(t *testing.T) {
	skillDir := makeSkillDir(t, "code-review")
	linkPath := filepath.Join(t.TempDir(), "link")

	if err := skill.CreateSymlink(linkPath, skillDir); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := skill.CreateSymlink(linkPath, skillDir); err != nil {
		t.Errorf("repeated CreateSymlink should be a no-op, got %v", err)
	}
}

func TestCreateSymlink_ReplaceWrongTarget(t *testing.T) {
	oldSkill := makeSkillDir(t, "code-review")
	newSkill := makeSkillDir(t, "code-review")
	linkPath := filepath.Join(t.TempDir(), "link")

	if err := skill.CreateSymlink(linkPath, oldSkill); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := skill.CreateSymlink(linkPath, newSkill); err != nil {
		t.Fatalf("replace: %v", err)
	}
	target, _ := os.Readlink(linkPath)
	absNew, _ := filepath.Abs(newSkill)
	if target != absNew {
		t.Errorf("target = %q, want %q", target, absNew)
	}
}

func TestCreateSymlink_RefusesToClobberFile(t *testing.T) {
	skillDir := makeSkillDir(t, "code-review")
	linkPath := filepath.Join(t.TempDir(), "link")
	if err := os.WriteFile(linkPath, []byte("precious"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	err := skill.CreateSymlink(linkPath, skillDir)
	if !errors.Is(err, skill.ErrSymlinkExists) {
		t.Errorf("expected ErrSymlinkExists, got %v", err)
	}
}

func TestCreateSymlink_MissingSkillDir(t *testing.T) {
	linkPath := filepath.Join(t.TempDir(), "link")
	err := skill.CreateSymlink(linkPath, filepath.Join(t.TempDir(), "nope"))
	if !errors.Is(err, skill.ErrTargetNotExist) {
		t.Errorf("expected ErrTargetNotExist, got %v", err)
	}
}

func TestCreateSymlink_NotASkill(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "fake"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	err := skill.CreateSymlink(filepath.Join(t.TempDir(), "link"), filepath.Join(dir, "fake"))
	if !errors.Is(err, skill.ErrTargetNotASkill) {
		t.Errorf("expected ErrTargetNotASkill, got %v", err)
	}
}

func TestRemoveSymlink(t *testing.T) {
	skillDir := makeSkillDir(t, "code-review")
	linkPath := filepath.Join(t.TempDir(), "link")
	if err := skill.CreateSymlink(linkPath, skillDir); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := skill.RemoveSymlink(linkPath); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Lstat(linkPath); !os.IsNotExist(err) {
		t.Errorf("symlink still present: %v", err)
	}
}

func TestRemoveSymlink_NotFound(t *testing.T) {
	err := skill.RemoveSymlink(filepath.Join(t.TempDir(), "ghost"))
	if !errors.Is(err, skill.ErrSymlinkNotFound) {
		t.Errorf("expected ErrSymlinkNotFound, got %v", err)
	}
}

func TestRemoveSymlink_RefusesRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(path, []byte{}, 0o644)
	err := skill.RemoveSymlink(path)
	if err == nil || errors.Is(err, skill.ErrSymlinkNotFound) {
		t.Errorf("expected non-symlink error, got %v", err)
	}
}

func TestVerifySymlink_OK(t *testing.T) {
	skillDir := makeSkillDir(t, "code-review")
	linkPath := filepath.Join(t.TempDir(), "link")
	_ = skill.CreateSymlink(linkPath, skillDir)
	if err := skill.VerifySymlink(linkPath); err != nil {
		t.Errorf("VerifySymlink: %v", err)
	}
}

func TestVerifySymlink_Broken(t *testing.T) {
	skillDir := makeSkillDir(t, "code-review")
	linkPath := filepath.Join(t.TempDir(), "link")
	_ = skill.CreateSymlink(linkPath, skillDir)
	// Break the symlink by deleting the target.
	_ = os.RemoveAll(skillDir)
	err := skill.VerifySymlink(linkPath)
	if !errors.Is(err, skill.ErrTargetNotExist) {
		t.Errorf("expected ErrTargetNotExist, got %v", err)
	}
}

func TestVerifySymlink_TargetMissingSkillMD(t *testing.T) {
	skillDir := makeSkillDir(t, "code-review")
	linkPath := filepath.Join(t.TempDir(), "link")
	_ = skill.CreateSymlink(linkPath, skillDir)
	_ = os.Remove(filepath.Join(skillDir, "SKILL.md"))
	err := skill.VerifySymlink(linkPath)
	if !errors.Is(err, skill.ErrTargetNotASkill) {
		t.Errorf("expected ErrTargetNotASkill, got %v", err)
	}
}

func TestVerifyTarget(t *testing.T) {
	skillA := makeSkillDir(t, "code-review")
	skillB := makeSkillDir(t, "code-review")
	linkPath := filepath.Join(t.TempDir(), "link")
	_ = skill.CreateSymlink(linkPath, skillA)

	if err := skill.VerifyTarget(linkPath, skillA); err != nil {
		t.Errorf("matching: %v", err)
	}
	err := skill.VerifyTarget(linkPath, skillB)
	if !errors.Is(err, skill.ErrSymlinkMismatch) {
		t.Errorf("expected ErrSymlinkMismatch, got %v", err)
	}
}

func TestResolveTargetPath(t *testing.T) {
	path, err := skill.ResolveTargetPath("claude", "code-review", "/proj", false)
	if err != nil {
		t.Fatalf("ResolveTargetPath: %v", err)
	}
	want := filepath.Join("/proj", ".claude/skills", "code-review")
	if path != want {
		t.Errorf("got %q, want %q", path, want)
	}
}

func TestResolveTargetPath_Global(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("home expansion not tested on Windows")
	}
	path, err := skill.ResolveTargetPath("claude", "code-review", "/proj", true)
	if err != nil {
		t.Fatalf("ResolveTargetPath: %v", err)
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".claude/skills", "code-review")
	if path != want {
		t.Errorf("got %q, want %q", path, want)
	}
}

func TestResolveTargetPath_UnknownTarget(t *testing.T) {
	_, err := skill.ResolveTargetPath("bogus", "x", "/proj", false)
	if !errors.Is(err, skill.ErrUnknownTarget) {
		t.Errorf("expected ErrUnknownTarget, got %v", err)
	}
}
