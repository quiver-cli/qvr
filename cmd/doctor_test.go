package cmd

import (
	"os"
	"path/filepath"
	"strings"
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

// Pre-v0.6.2 this asserted "any unrecognised symlink is `extra-symlink`
// (✗)". Issue #68 split that bucket: symlinks whose target is OUTSIDE
// the qvr-managed scope are now `orphan-external-symlink` (`!`), and
// only symlinks pointing INTO ~/.quiver/ but missing from the lock
// stay `extra-symlink`. writeFullSkill writes into an arbitrary tempdir
// outside QUIVER_HOME, so this orphan symlink falls into the external
// bucket; the test asserts the new categorisation. The other half of
// the policy (managed-target stays extra-symlink) is covered by
// TestScanExtraSymlinks_InsideScopeStillFailsAsExtra.
func TestDoctorChecks_OrphanExternalSymlinkSurfaced(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	project := t.TempDir()
	orphanSrc := writeFullSkill(t, "orphan")
	linkSkillInto(t, project, ".claude/skills", "orphan", orphanSrc)

	lock := model.NewLockFile(filepath.Join(project, model.LockFileName))

	checks := runDoctorChecks(lock, config.Default(), project)
	got := findCheck(t, checks, "orphan-external-symlink", "orphan")
	if got.Target != "claude" {
		t.Errorf("expected claude target, got %q", got.Target)
	}
	if !got.OK {
		t.Errorf("external-target symlinks should be informational (OK=true), got %+v", got)
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

// Regression for #62: two unreferenced worktrees that share a SHA prefix
// (the dspy-skills monorepo case where every skill rides one master SHA)
// must surface as distinct rows in `qvr doctor` — pre-fix the renderer
// collapsed both to the leaf segment, so the user couldn't tell them
// apart.
func TestDoctorChecks_OrphanWorktreePathDisambiguates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)

	// Two orphan worktrees with the same sha7 leaf but different
	// <registry>/<skill> parents.
	a := filepath.Join(registry.WorktreesRoot(), "dspy", "agent-builder", "5970598")
	b := filepath.Join(registry.WorktreesRoot(), "dspy", "evaluation-suite", "5970598")
	for _, p := range []string{a, b} {
		if err := os.MkdirAll(filepath.Join(p, ".git"), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}

	project := t.TempDir()
	lock := model.NewLockFile(filepath.Join(project, model.LockFileName))
	checks := scanOrphans(config.Default(), lock, false, project)

	var orphans []doctorCheck
	for _, c := range checks {
		if c.Type == "orphan-worktree" {
			orphans = append(orphans, c)
		}
	}
	if len(orphans) != 2 {
		t.Fatalf("want 2 orphan-worktree checks, got %d: %+v", len(orphans), orphans)
	}
	if orphans[0].Skill == orphans[1].Skill {
		t.Errorf("orphans collapsed to same label %q — should disambiguate via path", orphans[0].Skill)
	}
	for _, o := range orphans {
		// The label should carry the <registry>/<skill>/<sha7> segments,
		// not just the bare sha7.
		if !strings.Contains(o.Skill, "/") {
			t.Errorf("orphan label %q missing path separators — looks like a bare leaf", o.Skill)
		}
	}
}

// Regression for #60: `qvr doctor --global` must walk the user-home
// agent dirs (~/.claude/skills, …) when comparing against the global
// lock, not the surrounding project's dirs. Pre-fix it always walked
// projectRoot, so every project-scope symlink looked like an orphan
// against the global lock and broke `qvr doctor --global` in any repo
// with project skills installed alongside global ones.
func TestScanExtraSymlinks_GlobalScopeIgnoresProjectSymlinks(t *testing.T) {
	// Use HOME as the global root so the user-home dirs are isolated to
	// the test tempdir. expandHome resolves "~/.claude/skills" against
	// the t.Setenv-overridden HOME.
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Plant a project-scope symlink under the test project's
	// .claude/skills dir. Pre-#60 this would surface as extra-symlink
	// when scanning with global=true.
	project := t.TempDir()
	target := writeFullSkill(t, "project-skill")
	linkSkillInto(t, project, ".claude/skills", "project-skill", target)

	extras := scanExtraSymlinks(project, map[string]struct{}{}, true /*global*/)
	for _, e := range extras {
		if e.Skill == "project-skill" {
			t.Errorf("global-scope walk should not flag project-scope symlink as extra: %+v", e)
		}
	}
}

// Companion: in project scope, the same setup MUST surface the project
// symlink as an extra — confirms we didn't accidentally disable both
// paths.
func TestScanExtraSymlinks_ProjectScopeSurfacesProjectSymlinks(t *testing.T) {
	project := t.TempDir()
	target := writeFullSkill(t, "project-skill")
	linkSkillInto(t, project, ".claude/skills", "project-skill", target)

	extras := scanExtraSymlinks(project, map[string]struct{}{}, false /*global*/)
	var seen bool
	for _, e := range extras {
		if e.Skill == "project-skill" {
			seen = true
		}
	}
	if !seen {
		t.Errorf("project-scope walk should still flag the project-scope symlink: %+v", extras)
	}
}

// Regression for #68: a symlink in the agent dir whose target sits
// OUTSIDE the qvr-managed scope (e.g. into ~/.agents/skills/...,
// claudeskills.io, MCP-managed dirs) must surface as an informational
// orphan-external-symlink (`!` glyph, OK=true, no count against
// `lock-tracked checks failed`), matching `qvr sync`'s policy of
// "leave it alone, surface it." Pre-fix doctor hard-failed with `✗
// extra-symlink` on every such link, breaking `qvr doctor --global` in
// any user setup with mixed agent-dir managers.
func TestScanExtraSymlinks_OutsideScopeSurfacesAsInformational(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()

	// Plant a symlink under .claude/skills/external whose target sits
	// outside QUIVER_HOME — e.g. another tool's managed dir.
	external := filepath.Join(t.TempDir(), "another-tool", "find-skills")
	if err := os.MkdirAll(external, 0o755); err != nil {
		t.Fatalf("mkdir external: %v", err)
	}
	claudeDir := filepath.Join(project, ".claude", "skills")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}
	if err := os.Symlink(external, filepath.Join(claudeDir, "find-skills")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	extras := scanExtraSymlinks(project, map[string]struct{}{}, false /*global*/)
	var got *doctorCheck
	for i := range extras {
		if extras[i].Skill == "find-skills" {
			got = &extras[i]
		}
	}
	if got == nil {
		t.Fatalf("expected an entry for find-skills, got %+v", extras)
	}
	if got.Type != "orphan-external-symlink" {
		t.Errorf("type = %q, want orphan-external-symlink", got.Type)
	}
	if !got.OK {
		t.Errorf("external symlinks must be informational (OK=true), got %+v", got)
	}
	if !strings.Contains(got.Message, "outside qvr scope") {
		t.Errorf("message should mention outside qvr scope, got %q", got.Message)
	}
}

// Companion: a symlink whose target IS inside the qvr-managed scope
// (the worktrees cache) but isn't named in the lock should STILL surface
// as an extra-symlink (✗) — that's the real orphan sync would prune.
func TestScanExtraSymlinks_InsideScopeStillFailsAsExtra(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	project := t.TempDir()

	// Target lives inside ~/.quiver/worktrees (a real qvr-managed dir).
	managed := filepath.Join(home, "worktrees", "fake", "demo", "abc1234")
	if err := os.MkdirAll(managed, 0o755); err != nil {
		t.Fatalf("mkdir managed: %v", err)
	}
	claudeDir := filepath.Join(project, ".claude", "skills")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}
	if err := os.Symlink(managed, filepath.Join(claudeDir, "orphan-managed")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	extras := scanExtraSymlinks(project, map[string]struct{}{}, false /*global*/)
	var got *doctorCheck
	for i := range extras {
		if extras[i].Skill == "orphan-managed" {
			got = &extras[i]
		}
	}
	if got == nil {
		t.Fatalf("expected an entry for orphan-managed, got %+v", extras)
	}
	if got.Type != "extra-symlink" {
		t.Errorf("type = %q, want extra-symlink", got.Type)
	}
	if got.OK {
		t.Errorf("a managed-but-unlocked symlink must hard-fail, got OK=true")
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
