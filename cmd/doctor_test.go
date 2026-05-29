package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/registry"
)

// findCheck returns the first check matching type and skill, or fails the test.
func findCheck(t *testing.T, checks []doctorCheck, ckType, skill string) doctorCheck {
	t.Helper()
	for _, c := range checks {
		if c.Type == ckType && c.Skill == skill {
			return c
		}
	}
	t.Fatalf("no check found type=%s skill=%s in %+v", ckType, skill, checks)
	return doctorCheck{}
}

func TestDoctorChecks_AllGreen(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())

	// Use a v5 registry install (not link), so the worktree check exists.
	// Seed a real worktree at the derived path with a skill directory the
	// symlink can target.
	reg, name, commit := "acme", "demo", "abc1234"
	worktree := registry.WorktreePath(reg, name, registry.ShortSHA(commit))
	skillRel := filepath.Join("skills", "demo")
	skillDir := filepath.Join(worktree, skillRel)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: demo\ndescription: x\n---\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	project := t.TempDir()
	linkSkillInto(t, project, ".claude/skills", "demo", skillDir)

	lock := model.NewLockFile(filepath.Join(project, model.LockFileName))
	lock.Put(&model.LockEntry{
		Name:     name,
		Registry: reg,
		Source:   "git@example.test:" + reg + ".git",
		Path:     skillRel,
		Ref:      "main",
		Commit:   commit,
		Targets:  []string{"claude"},
	})

	cfg := config.Default()
	checks := runDoctorChecks(lock, cfg, project)

	wt := findCheck(t, checks, "worktree", "demo")
	if !wt.OK {
		t.Errorf("worktree check should pass: %+v", wt)
	}
	sym := findCheck(t, checks, "symlink", "demo")
	if !sym.OK {
		t.Errorf("symlink check should pass: %+v", sym)
	}
}

// TestDoctorChecks_HonorsSkillPath pins the bug #12 fix: doctor must accept
// symlinks pointing at `<worktree>/<Path>` — the shape `qvr install`
// actually creates. Pre-fix, doctor compared against `entry.Worktree`
// (worktree root) and failed with `target mismatch` immediately after a
// clean install, breaking CI usage.
func TestDoctorChecks_HonorsSkillPath(t *testing.T) {
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
	linkSkillInto(t, project, ".claude/skills", "demo", skillDir)

	lock := model.NewLockFile(filepath.Join(project, model.LockFileName))
	lock.Put(&model.LockEntry{
		Name:     "demo",
		Registry: reg,
		Source:   "git@example.test:" + reg + ".git",
		Path:     "skills/demo",
		Ref:      "main",
		Commit:   commit,
		Targets:  []string{"claude"},
	})

	checks := runDoctorChecks(lock, config.Default(), project)
	sym := findCheck(t, checks, "symlink", "demo")
	if !sym.OK {
		t.Errorf("symlink should pass with link pointing at leaf: %+v", sym)
	}
}

func TestDoctorChecks_BrokenWorktree(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	project := t.TempDir()
	lock := model.NewLockFile(filepath.Join(project, model.LockFileName))
	// Registry+Commit point at a path WorktreePath will compute but that
	// does not exist on disk — doctor should flag it as broken.
	lock.Put(&model.LockEntry{
		Name:     "ghost",
		Registry: "ghost-reg",
		Source:   "git@x:ghost.git",
		Ref:      "main",
		Commit:   "deadbeef000000000000000000000000000000",
		Targets:  []string{"claude"},
	})

	checks := runDoctorChecks(lock, config.Default(), project)
	wt := findCheck(t, checks, "worktree", "ghost")
	if wt.OK {
		t.Errorf("worktree should be broken: %+v", wt)
	}
	if wt.Message == "" {
		t.Error("expected a message for broken worktree")
	}
}

func TestDoctorChecks_BrokenSymlink(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	_ = writeFullSkill(t, "demo")
	project := t.TempDir()

	otherSrc := writeFullSkill(t, "demo")
	linkSkillInto(t, project, ".claude/skills", "demo", otherSrc)

	lock := model.NewLockFile(filepath.Join(project, model.LockFileName))
	lock.Put(&model.LockEntry{
		Name:    "demo",
		Targets: []string{"claude"},
	})
	checks := runDoctorChecks(lock, config.Default(), project)

	sym := findCheck(t, checks, "symlink", "demo")
	if sym.OK {
		t.Errorf("symlink mismatch should fail: %+v", sym)
	}
}

func TestDoctorChecks_RegistryMissing(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	_ = writeFullSkill(t, "demo")
	project := t.TempDir()

	lock := model.NewLockFile(filepath.Join(project, model.LockFileName))
	lock.Put(&model.LockEntry{
		Name:     "demo",
		Registry: "ghost-reg",
		Targets:  []string{},
	})

	checks := runDoctorChecks(lock, config.Default(), project)
	reg := findCheck(t, checks, "registry", "demo")
	if reg.OK {
		t.Errorf("registry check should fail when reg is unknown: %+v", reg)
	}
}

// TestDoctorChecks_SubdirSourceSkipsRegistry pins the fix for the doctor
// false-positive on `qvr add` installs. Source == "subdir" entries
// intentionally don't appear in cfg.Registries — the bare clone is owned by
// the lock entry — so the registry-config check must be skipped, not failed.
func TestDoctorChecks_SubdirSourceSkipsRegistry(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	reg, name, commit := "github.com--mattpocock--skills", "demo", "abc1234"

	// Seed the derived worktree path so the worktree check sees a real dir.
	wtPath := registry.WorktreePath(reg, name, registry.ShortSHA(commit))
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	src := writeFullSkill(t, "demo")
	project := t.TempDir()
	linkSkillInto(t, project, ".claude/skills", "demo", src)

	lock := model.NewLockFile(filepath.Join(project, model.LockFileName))
	// v5: source URL on a remote-installed entry (no separate "subdir" enum).
	// Registry is set but not in cfg — should not trip the registry check
	// because Source carries the fetch URL on every v5 entry.
	lock.Put(&model.LockEntry{
		Name:     name,
		Registry: reg,
		Source:   "https://github.com/mattpocock/skills.git",
		Ref:      "main",
		Commit:   commit,
		Targets:  []string{"claude"},
	})

	checks := runDoctorChecks(lock, config.Default(), project)
	for _, c := range checks {
		if c.Type == "registry" {
			t.Errorf("v5 entries with source URL must not produce a registry check, got %+v", c)
		}
	}
	wt := findCheck(t, checks, "worktree", "demo")
	if !wt.OK {
		t.Errorf("worktree check should still run and pass: %+v", wt)
	}
}

func TestDoctorChecks_ExtraSymlinkSurfaced(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	project := t.TempDir()
	orphanSrc := writeFullSkill(t, "orphan")
	linkSkillInto(t, project, ".claude/skills", "orphan", orphanSrc)

	lock := model.NewLockFile(filepath.Join(project, model.LockFileName))

	checks := runDoctorChecks(lock, config.Default(), project)
	got := findCheck(t, checks, "extra-symlink", "orphan")
	if got.Target != "claude" {
		t.Errorf("expected claude target, got %q", got.Target)
	}
	if got.OK {
		t.Errorf("extra symlinks should be reported as not OK: %+v", got)
	}
}

// TestScanUnreferencedRegistries_FlagsConfiguredButUnused: a registry that
// lives in cfg.Registries but isn't named by any lock entry should surface
// as an informational unreferenced-registry check. This is the Phase 4
// cleanup-prompt the user sees when they've removed every skill from a
// registry but forgot to `qvr registry remove` it.
func TestScanUnreferencedRegistries_FlagsConfiguredButUnused(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())

	cfg := config.Default()
	cfg.Registries["unused"] = config.RegistryConfig{URL: "https://example/unused"}
	cfg.Registries["used"] = config.RegistryConfig{URL: "https://example/used"}

	project := t.TempDir()
	lock := model.NewLockFile(filepath.Join(project, model.LockFileName))
	lock.Put(&model.LockEntry{
		Name:     "demo",
		Registry: "used",
		Targets:  []string{"claude"},
	})
	locks := []scopedLock{{Scope: "project", Lock: lock}}

	checks := scanUnreferencedRegistries(cfg, locks, project)
	var got *doctorCheck
	for i := range checks {
		if checks[i].Skill == "unused" {
			got = &checks[i]
		}
		if checks[i].Skill == "used" {
			t.Errorf("referenced registry %q should not appear: %+v", "used", checks[i])
		}
	}
	if got == nil {
		t.Fatalf("expected unreferenced-registry check for %q, got %+v", "unused", checks)
	}
	if got.Type != "unreferenced-registry" {
		t.Errorf("type = %q, want unreferenced-registry", got.Type)
	}
	if !got.OK {
		t.Errorf("unreferenced-registry should default to informational (OK=true), got %+v", got)
	}
}

func TestDoctorChecks_LinkInstallSkipped(t *testing.T) {
	project := t.TempDir()
	t.Setenv("QUIVER_HOME", t.TempDir())

	lock := model.NewLockFile(filepath.Join(project, model.LockFileName))
	lock.Put(&model.LockEntry{
		Name:    "linked",
		Source:  "/some/where",
		Ref:     "local",
		Targets: []string{"claude"},
	})
	checks := runDoctorChecks(lock, config.Default(), project)
	for _, c := range checks {
		if c.Skill == "linked" {
			t.Errorf("link installs should be skipped, got check: %+v", c)
		}
	}

	if _, err := os.Stat(project); err != nil {
		t.Fatalf("project should exist: %v", err)
	}
}
