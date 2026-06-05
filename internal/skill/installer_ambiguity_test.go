package skill_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/skill"
)

// sharedSkill is a minimal valid skill whose canonical name is "shared",
// reused across two registries to exercise the ambiguous-resolution code
// paths from issue #101.
const sharedSkill = `---
name: shared
description: same name across multiple registries
---

# shared
`

// TestInstall_AmbiguousName_NoRefWarns covers the first half of issue #101:
// `qvr add shared` with two registries that both expose "shared" should not
// silently pick one — it should pick alphabetically and surface a warning so
// the user can rescope with --registry.
func TestInstall_AmbiguousName_NoRefWarns(t *testing.T) {
	h := newHarness(t)
	remoteA := seedRemote(t, map[string]string{"shared": sharedSkill})
	remoteB := seedRemote(t, map[string]string{"shared": sharedSkill})
	h.addRegistry(t, "alpha", remoteA)
	h.addRegistry(t, "beta", remoteB)

	result, err := h.installer.Install(skill.InstallRequest{
		Skill:       "shared",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if result.Registry != "alpha" {
		t.Errorf("Registry = %q, want alpha (alphabetical pick)", result.Registry)
	}
	if len(result.Warnings) == 0 {
		t.Fatalf("expected at least one ambiguity warning, got none")
	}
	got := result.Warnings[0]
	for _, want := range []string{"shared", "alpha", "beta", "--registry"} {
		if !strings.Contains(got, want) {
			t.Errorf("warning missing %q: %s", want, got)
		}
	}
}

// TestInstall_AmbiguousName_NoWarningWhenScoped confirms that --registry
// silences the ambiguity warning even when the name resolves in multiple
// registries.
func TestInstall_AmbiguousName_NoWarningWhenScoped(t *testing.T) {
	h := newHarness(t)
	remoteA := seedRemote(t, map[string]string{"shared": sharedSkill})
	remoteB := seedRemote(t, map[string]string{"shared": sharedSkill})
	h.addRegistry(t, "alpha", remoteA)
	h.addRegistry(t, "beta", remoteB)

	result, err := h.installer.Install(skill.InstallRequest{
		Skill:       "shared",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
		Registry:    "beta",
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if result.Registry != "beta" {
		t.Errorf("Registry = %q, want beta", result.Registry)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("expected no warnings when --registry scoped, got %v", result.Warnings)
	}
}

// TestInstall_AmbiguousName_PicksRegistryWithRef is the wrong-pick-then-error
// regression for issue #101. Both registries expose "shared", but only "beta"
// has v2.0.0. The old resolver would silently pick "alpha" alphabetically and
// then error with "reference not found"; the fix should walk candidates and
// land on "beta" without a warning (the ref disambiguated for us).
func TestInstall_AmbiguousName_PicksRegistryWithRef(t *testing.T) {
	h := newHarness(t)
	// alpha has only v1; beta has v1 + v2. Naming forces alpha to sort
	// first so a non-ref-aware resolver would pick the wrong registry.
	remoteA := seedRemoteWithTags(t, map[string]string{"shared": sharedSkill}, "v1.0.0")
	remoteB := seedRemoteWithTags(t, map[string]string{"shared": sharedSkill}, "v1.0.0", "v2.0.0")
	h.addRegistry(t, "alpha", remoteA)
	h.addRegistry(t, "beta", remoteB)

	result, err := h.installer.Install(skill.InstallRequest{
		Skill:       "shared@v2.0.0",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	})
	if err != nil {
		t.Fatalf("install shared@v2.0.0: %v", err)
	}
	if result.Registry != "beta" {
		t.Errorf("Registry = %q, want beta (the only one with v2.0.0)", result.Registry)
	}
	if result.Version != "v2.0.0" {
		t.Errorf("Version = %q, want v2.0.0", result.Version)
	}
	// Ref-disambiguated picks should not warn — the user gave us enough
	// to resolve unambiguously.
	if len(result.Warnings) != 0 {
		t.Errorf("expected no warnings for ref-resolved pick, got %v", result.Warnings)
	}
}

// TestInstall_AmbiguousName_RefMissingErrorsHelpfully covers the case where
// the requested @<ref> isn't in any registry that exposes the skill. The
// error must be typed (ErrAmbiguousRef so cmd/add.go can special-case it),
// must name the ref, and must list per-registry versions so the user knows
// where to rescope.
func TestInstall_AmbiguousName_RefMissingErrorsHelpfully(t *testing.T) {
	h := newHarness(t)
	remoteA := seedRemoteWithTags(t, map[string]string{"shared": sharedSkill}, "v1.0.0")
	remoteB := seedRemoteWithTags(t, map[string]string{"shared": sharedSkill}, "v1.0.0")
	h.addRegistry(t, "alpha", remoteA)
	h.addRegistry(t, "beta", remoteB)

	_, err := h.installer.Install(skill.InstallRequest{
		Skill:       "shared@v9.9.9",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	})
	if err == nil {
		t.Fatal("expected error for missing ref across registries")
	}
	if !errors.Is(err, skill.ErrAmbiguousRef) {
		t.Errorf("expected ErrAmbiguousRef, got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"v9.9.9", "alpha", "beta", "v1.0.0", "--registry"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %s", want, msg)
		}
	}
}

// TestInstall_ConflictHint_PointsAtRemoveAndAdd guards issue #111: the
// conflict error message when re-adding a skill at a different ref must
// not lead with `qvr switch <name> <ref>` — that's only correct for
// same-source ref changes and is misleading for cross-registry alias
// collisions. The hint must surface remove+add and --force as the
// always-correct recovery paths.
func TestInstall_ConflictHint_PointsAtRemoveAndAdd(t *testing.T) {
	h := newHarness(t)
	remote := seedRemoteWithTags(t, map[string]string{"shared": sharedSkill}, "v1.0.0", "v2.0.0")
	h.addRegistry(t, "acme", remote)

	if _, err := h.installer.Install(skill.InstallRequest{
		Skill:       "shared@v1.0.0",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	}); err != nil {
		t.Fatalf("first install: %v", err)
	}
	// Same alias, different ref → conflict.
	_, err := h.installer.Install(skill.InstallRequest{
		Skill:       "shared@v2.0.0",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	})
	if err == nil {
		t.Fatal("expected conflict error on re-install at different ref, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"--force", "qvr remove", "qvr add", "v1.0.0", "v2.0.0"} {
		if !strings.Contains(msg, want) {
			t.Errorf("conflict hint missing %q: %s", want, msg)
		}
	}
	// The old hint led with `qvr switch <name> v2.0.0` as the primary
	// recommendation. v0.8.8 demotes it: it now only appears as a
	// qualifier ("`qvr switch` only moves the ref within the same
	// source") — never as the leading verb in the message.
	if strings.Index(msg, "use `qvr switch") < strings.Index(msg, "qvr remove") && strings.Contains(msg, "use `qvr switch") {
		t.Errorf("conflict hint still leads with `qvr switch`: %s", msg)
	}
}

// TestInstall_AmbiguousRef_WarnsWhenMultipleHaveRef covers issue #106:
// when two registries both expose "shared" AND both carry v1.0.0, the @ref
// path used to silently pick alphabetical without surfacing the same
// ambiguity warning the bare-name path emits. The fix harmonises the two:
// multiple ref-matches → warn + alphabetical pick (1 → silent; 0 →
// ErrAmbiguousRef, the existing behavior).
func TestInstall_AmbiguousRef_WarnsWhenMultipleHaveRef(t *testing.T) {
	h := newHarness(t)
	remoteA := seedRemoteWithTags(t, map[string]string{"shared": sharedSkill}, "v1.0.0")
	remoteB := seedRemoteWithTags(t, map[string]string{"shared": sharedSkill}, "v1.0.0")
	h.addRegistry(t, "alpha", remoteA)
	h.addRegistry(t, "beta", remoteB)

	result, err := h.installer.Install(skill.InstallRequest{
		Skill:       "shared@v1.0.0",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	})
	if err != nil {
		t.Fatalf("install shared@v1.0.0: %v", err)
	}
	if result.Registry != "alpha" {
		t.Errorf("Registry = %q, want alpha (alphabetical pick)", result.Registry)
	}
	if len(result.Warnings) == 0 {
		t.Fatalf("expected ambiguity warning for shared@v1.0.0 across two registries, got none")
	}
	got := result.Warnings[0]
	for _, want := range []string{"shared", "v1.0.0", "alpha", "beta", "--registry"} {
		if !strings.Contains(got, want) {
			t.Errorf("warning missing %q: %s", want, got)
		}
	}
}

// TestInstall_AsAlias is the basic happy path for the new --as flag: an
// alias install puts the lock entry and symlink at the alias name, while
// the underlying worktree stays keyed by the canonical skill name + SHA.
func TestInstall_AsAlias(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)

	result, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
		As:          "review-old",
	})
	if err != nil {
		t.Fatalf("install --as: %v", err)
	}
	if result.Name != "review-old" {
		t.Errorf("Name = %q, want review-old", result.Name)
	}
	if result.Canonical != "code-review" {
		t.Errorf("Canonical = %q, want code-review", result.Canonical)
	}
	// Symlink should be at the alias name.
	if _, err := os.Lstat(filepath.Join(h.project, ".claude/skills/review-old")); err != nil {
		t.Errorf("alias symlink missing: %v", err)
	}
	// Lock entry should be keyed by the alias.
	lock, err := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get("review-old")
	if err != nil {
		t.Fatalf("lock missing alias entry: %v", err)
	}
	if entry.Canonical != "code-review" {
		t.Errorf("entry.Canonical = %q, want code-review", entry.Canonical)
	}
	if entry.Path != "skills/code-review" {
		t.Errorf("entry.Path = %q, want skills/code-review", entry.Path)
	}
}

// TestInstall_AsAlias_CoexistsWithCanonical confirms the A/B-testing use
// case: a canonical install and an aliased install of the same skill (at
// different refs) can live side-by-side in one project lock.
func TestInstall_AsAlias_CoexistsWithCanonical(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill}, "v2")
	h.addRegistry(t, "acme", remote)

	if _, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review@main",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	}); err != nil {
		t.Fatalf("install canonical: %v", err)
	}
	if _, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review@v2",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
		As:          "code-review-v2",
	}); err != nil {
		t.Fatalf("install alias: %v", err)
	}

	lock, err := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	if _, err := lock.Get("code-review"); err != nil {
		t.Errorf("canonical entry missing: %v", err)
	}
	if _, err := lock.Get("code-review-v2"); err != nil {
		t.Errorf("alias entry missing: %v", err)
	}
	// Both symlinks should resolve (different filenames, no clash).
	for _, name := range []string{"code-review", "code-review-v2"} {
		if _, err := os.Lstat(filepath.Join(h.project, ".claude/skills", name)); err != nil {
			t.Errorf("symlink %s missing: %v", name, err)
		}
	}
}

// TestInstall_AsAlias_InvalidName refuses --as values that violate the
// agentskills.io name spec — same rules the validator enforces on canonical
// names, since the alias is what `qvr remove`/`qvr list` will surface.
func TestInstall_AsAlias_InvalidName(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)

	for _, bad := range []string{"UPPERCASE", "with space", "double--hyphen", "-leading"} {
		_, err := h.installer.Install(skill.InstallRequest{
			Skill:       "code-review",
			Targets:     []string{"claude"},
			ProjectRoot: h.project,
			As:          bad,
		})
		if err == nil {
			t.Errorf("expected error for invalid --as %q", bad)
			continue
		}
		if !strings.Contains(err.Error(), "invalid --as") {
			t.Errorf("error for %q missing 'invalid --as': %v", bad, err)
		}
	}
}
