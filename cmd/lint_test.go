package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/registry"
)

// Issue #64: `qvr validate <name>` should resolve a bare installed-skill
// name through the lock the same way `qvr scan` does, instead of surfacing
// a raw `stat: no such file or directory`. Exercises resolveSkillArg via
// the lock path (no chdir, name not present in cwd).
func TestResolveSkillArg_ResolvesInstalledNameViaLock(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	reg, name, commit := "vr", "demo", "abc1234"
	worktree := registry.WorktreePath(reg, name, registry.ShortSHA(commit))
	skillRel := filepath.Join("skills", "demo")
	writeSkillAt(t, filepath.Join(worktree, skillRel), name)

	project := t.TempDir()
	prev, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(prev) })
	if err := os.Chdir(project); err != nil {
		t.Fatalf("chdir project: %v", err)
	}

	lock := model.NewLockFile(filepath.Join(project, model.LockFileName))
	lock.Put(&model.LockEntry{
		Name:     name,
		Registry: reg,
		Source:   "git@example.test:" + reg + ".git",
		Path:     skillRel,
		Ref:      "main",
		Commit:   commit,
	})
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	resolved, _, err := resolveSkillArg(name, false /*global*/)
	if err != nil {
		t.Fatalf("expected lock resolution for bare name, got error: %v", err)
	}
	want := filepath.Join(worktree, skillRel)
	if resolved != want {
		t.Errorf("resolved = %q, want %q", resolved, want)
	}
}

// Path-like positionals (./foo, /abs, foo/bar) must always go through
// the filesystem resolver — the lock fallback is only for bare names.
func TestResolveSkillArg_PathArgUsesFilesystem(t *testing.T) {
	root := t.TempDir()
	writeSkillAt(t, root, "demo")

	resolved, _, err := resolveSkillArg(root, false)
	if err != nil {
		t.Fatalf("path arg should resolve via filesystem: %v", err)
	}
	if resolved != root {
		t.Errorf("resolved = %q, want %q", resolved, root)
	}
}

func writeSkillAt(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "---\nname: " + name + "\ndescription: test skill\n---\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestResolveSkillDir_RootSkill(t *testing.T) {
	root := t.TempDir()
	writeSkillAt(t, root, "demo")

	resolved, _, err := resolveSkillDir(root)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if resolved != root {
		t.Errorf("want %q, got %q", root, resolved)
	}
}

func TestResolveSkillDir_RegistryLayout_SingleSkill(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "deploy-helper")
	writeSkillAt(t, skillDir, "deploy-helper")

	resolved, discovered, err := resolveSkillDir(root)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if resolved != skillDir {
		t.Errorf("want %q, got %q", skillDir, resolved)
	}
	if len(discovered) != 1 || discovered[0] != skillDir {
		t.Errorf("discovered = %v, want [%s]", discovered, skillDir)
	}
}

func TestResolveSkillDir_RegistryLayout_Multiple(t *testing.T) {
	root := t.TempDir()
	writeSkillAt(t, filepath.Join(root, "skills", "alpha"), "alpha")
	writeSkillAt(t, filepath.Join(root, "skills", "beta"), "beta")

	resolved, discovered, err := resolveSkillDir(root)
	if err == nil {
		t.Fatal("expected an error for ambiguous registry layout")
	}
	if resolved != "" {
		t.Errorf("expected empty resolved path, got %q", resolved)
	}
	if len(discovered) != 2 {
		t.Fatalf("expected 2 discovered, got %d", len(discovered))
	}
	msg := err.Error()
	for _, want := range []string{"alpha", "beta", "found 2 skills"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

func TestResolveSkillDir_NoSkill(t *testing.T) {
	root := t.TempDir()
	_, discovered, err := resolveSkillDir(root)
	if err == nil {
		t.Fatal("expected error when no SKILL.md anywhere")
	}
	if len(discovered) != 0 {
		t.Errorf("expected no discoveries, got %v", discovered)
	}
	msg := err.Error()
	if !strings.Contains(msg, "SKILL.md not found") {
		t.Errorf("error should mention SKILL.md, got %q", msg)
	}
	if !strings.Contains(msg, "skills/") {
		t.Errorf("error should hint at skills/ layout, got %q", msg)
	}
}

func TestResolveSkillDir_FixtureRegistry(t *testing.T) {
	root := filepath.Join("..", "testdata", "sample-registry")
	resolved, discovered, err := resolveSkillDir(root)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.HasSuffix(resolved, filepath.Join("skills", "example-skill")) {
		t.Errorf("resolved should land on example-skill, got %q", resolved)
	}
	if len(discovered) != 1 {
		t.Errorf("expected 1 discovered, got %d", len(discovered))
	}
}
