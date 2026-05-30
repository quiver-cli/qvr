package skill_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/registry"
	"github.com/raks097/quiver/internal/skill"
)

// TestEntryWorktreePath_PrefersCanonical confirms the read-side worktree
// path derivation matches the install-side path when the entry was created
// via `qvr add --as <alias>`. Without this, info/status/diff/edit/doctor
// all look at .../reg/<alias>/<sha> while the actual worktree sits at
// .../reg/<canonical>/<sha>. Regression for issue #102.
func TestEntryWorktreePath_PrefersCanonical(t *testing.T) {
	testEnv(t)
	entry := &model.LockEntry{
		Name:          "my-info",
		Registry:      "reg-a",
		Canonical:     "shared",
		InstallCommit: "abc1234deadbeef",
	}
	got := skill.EntryWorktreePath(entry)
	want := registry.WorktreePath("reg-a", "shared", registry.ShortSHA("abc1234deadbeef"))
	if got != want {
		t.Errorf("EntryWorktreePath(aliased) = %q, want %q (canonical-keyed)", got, want)
	}
}

// TestEntryWorktreePath_FallsBackToName covers the canonical-not-set path:
// non-aliased entries (the common case) continue to key by entry.Name.
func TestEntryWorktreePath_FallsBackToName(t *testing.T) {
	testEnv(t)
	entry := &model.LockEntry{
		Name:          "shared",
		Registry:      "reg-a",
		InstallCommit: "abc1234deadbeef",
	}
	got := skill.EntryWorktreePath(entry)
	want := registry.WorktreePath("reg-a", "shared", registry.ShortSHA("abc1234deadbeef"))
	if got != want {
		t.Errorf("EntryWorktreePath(no alias) = %q, want %q", got, want)
	}
}

// TestInstall_AsAlias_EntryWorktreePathPointsAtRealDir is the end-to-end
// regression: install with --as, then derive the worktree path via the
// public read-side helper and confirm SKILL.md is reachable. This is what
// info/status/diff/edit/doctor all rely on.
func TestInstall_AsAlias_EntryWorktreePathPointsAtRealDir(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)

	if _, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
		As:          "review-old",
	}); err != nil {
		t.Fatalf("install --as: %v", err)
	}

	lock, err := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get("review-old")
	if err != nil {
		t.Fatalf("lock get: %v", err)
	}

	worktree := skill.EntryWorktreePath(entry)
	if worktree == "" {
		t.Fatal("EntryWorktreePath returned empty")
	}
	// The alias must not leak into the worktree path — that was the #102 bug.
	if strings.Contains(worktree, string(os.PathSeparator)+"review-old"+string(os.PathSeparator)) {
		t.Errorf("worktree path contains alias %q: %s", "review-old", worktree)
	}
	skillMD := filepath.Join(worktree, entry.Path, "SKILL.md")
	if _, err := os.Stat(skillMD); err != nil {
		t.Errorf("SKILL.md not reachable via EntryWorktreePath: %v (path=%s)", err, skillMD)
	}
}

// TestInstall_Frozen_SuppressesAmbiguityWarning is the #105 regression:
// when --frozen is set, the lockfile's recorded registry is authoritative
// for the install, so resolveSkill must be scoped to that registry. The
// pre-fix behaviour walked every configured registry and surfaced a
// "shared resolves to 2 registries" warning even though the lockfile had
// already pinned reg-a.
func TestInstall_Frozen_SuppressesAmbiguityWarning(t *testing.T) {
	h := newHarness(t)
	remoteA := seedRemote(t, map[string]string{"shared": sharedSkill})
	remoteB := seedRemote(t, map[string]string{"shared": sharedSkill})
	h.addRegistry(t, "alpha", remoteA)
	h.addRegistry(t, "beta", remoteB)

	// Pin the install to alpha via --registry so the lock entry records it.
	if _, err := h.installer.Install(skill.InstallRequest{
		Skill:       "shared",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
		Registry:    "alpha",
		As:          "my-pin",
	}); err != nil {
		t.Fatalf("install --registry alpha --as my-pin: %v", err)
	}

	// Frozen restore by alias — must NOT emit the multi-registry warning
	// because the lockfile already pinned the registry.
	result, err := h.installer.Install(skill.InstallRequest{
		Skill:       "my-pin",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
		Frozen:      true,
	})
	if err != nil {
		t.Fatalf("install --frozen my-pin: %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("expected no warnings under --frozen, got %v", result.Warnings)
	}
	if result.Registry != "alpha" {
		t.Errorf("Registry = %q, want alpha (pinned by lock)", result.Registry)
	}
}

// TestInstall_Frozen_SuppressesAmbiguityWarning_Canonical covers the
// non-aliased variant of #105: even without --as, a frozen install of a
// canonically-named skill should not re-walk every registry when the lock
// records which one it came from.
func TestInstall_Frozen_SuppressesAmbiguityWarning_Canonical(t *testing.T) {
	h := newHarness(t)
	remoteA := seedRemote(t, map[string]string{"shared": sharedSkill})
	remoteB := seedRemote(t, map[string]string{"shared": sharedSkill})
	h.addRegistry(t, "alpha", remoteA)
	h.addRegistry(t, "beta", remoteB)

	if _, err := h.installer.Install(skill.InstallRequest{
		Skill:       "shared",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
		Registry:    "beta",
	}); err != nil {
		t.Fatalf("install --registry beta: %v", err)
	}

	result, err := h.installer.Install(skill.InstallRequest{
		Skill:       "shared",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
		Frozen:      true,
	})
	if err != nil {
		t.Fatalf("install --frozen shared: %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("expected no warnings under --frozen (canonical), got %v", result.Warnings)
	}
	if result.Registry != "beta" {
		t.Errorf("Registry = %q, want beta", result.Registry)
	}
}

// TestInstall_Frozen_ByAlias confirms that `qvr add --frozen <alias>` looks
// the alias up in the lock, swaps to the canonical name for registry
// resolution, and preserves the alias as the lock key. The pre-fix
// behaviour was ErrSkillNotFound — the resolver treated the alias as a
// registry skill name. Issue #102.
func TestInstall_Frozen_ByAlias(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)

	// First install creates the aliased lock entry.
	if _, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
		As:          "review-old",
	}); err != nil {
		t.Fatalf("install --as: %v", err)
	}

	// Frozen restore using the alias name — must succeed against the lock.
	result, err := h.installer.Install(skill.InstallRequest{
		Skill:       "review-old",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
		Frozen:      true,
	})
	if err != nil {
		t.Fatalf("install --frozen by alias: %v", err)
	}
	if result.Name != "review-old" {
		t.Errorf("Name = %q, want review-old (alias preserved)", result.Name)
	}
	if result.Canonical != "code-review" {
		t.Errorf("Canonical = %q, want code-review", result.Canonical)
	}

	// Lock should still have exactly one entry keyed by the alias.
	lock, err := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	if _, err := lock.Get("review-old"); err != nil {
		t.Errorf("alias entry missing after frozen restore: %v", err)
	}
	if _, err := lock.Get("code-review"); err == nil {
		t.Error("frozen restore wrongly created a canonical-named entry alongside the alias")
	}
}
