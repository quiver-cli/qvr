package skill_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	gogit "github.com/go-git/go-git/v5"

	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/skill"
)

// seedSharedWorktree fakes the on-disk state of a shared-mode install: a
// worktree dir under the test's QUIVER_HOME with a SKILL.md inside it. Returns
// the lock entry whose EntryWorktreePath() points at the fake dir. Skips the
// registry/git plumbing entirely — eject only cares about the directory
// existing and containing SKILL.md.
func seedSharedWorktreeForEject(t *testing.T, name, registryName string) *model.LockEntry {
	t.Helper()
	quiverHome := testEnv(t)

	// Mirror the worktree path layout: <quiverHome>/worktrees/<registry>/<name>/<sha7>/
	const fakeSHA = "abcdef0123456789abcdef0123456789abcdef01"
	worktreeDir := filepath.Join(quiverHome, "worktrees", registryName, name, fakeSHA[:7])
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	skillMD := "---\nname: " + name + "\ndescription: An ejectable test skill used by the eject test.\n---\n\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(worktreeDir, "SKILL.md"), []byte(skillMD), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	return &model.LockEntry{
		Name:          name,
		Registry:      registryName,
		Source:        initEmptyBareWithHEAD(t, registryName, "main"),
		Ref:           "main",
		Commit:        fakeSHA,
		InstallCommit: fakeSHA,
		Targets:       []string{"claude"},
	}
}

// TestEjectToTarget_SingleTarget covers the most common case: a skill installed
// for one agent (claude) gets ejected into the project's `.claude/skills/`,
// the lock entry flips to edit mode with the expected fields, and a fresh git
// history is initialised inside the new dir.
func TestEjectToTarget_SingleTarget(t *testing.T) {
	entry := seedSharedWorktreeForEject(t, "demo", "raks")
	originalSource := entry.Source
	projectRoot := t.TempDir()

	result, err := skill.EjectToTarget(skill.EjectRequest{
		Entry:       entry,
		ProjectRoot: projectRoot,
	})
	if err != nil {
		t.Fatalf("EjectToTarget: %v", err)
	}

	canonical := filepath.Join(projectRoot, ".claude", "skills", "demo")
	if result.EditPath != ".claude/skills/demo" {
		t.Errorf("EditPath = %q, want %q", result.EditPath, ".claude/skills/demo")
	}
	if result.CanonicalTarget != "claude" {
		t.Errorf("CanonicalTarget = %q, want claude", result.CanonicalTarget)
	}

	// Real directory, not a symlink.
	info, err := os.Lstat(canonical)
	if err != nil {
		t.Fatalf("lstat canonical: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Errorf("canonical %s is a symlink, expected a real directory", canonical)
	}
	if !info.IsDir() {
		t.Errorf("canonical %s is not a directory", canonical)
	}

	// SKILL.md copied through.
	skillBytes, err := os.ReadFile(filepath.Join(canonical, "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if !strings.Contains(string(skillBytes), "name: demo") {
		t.Errorf("SKILL.md content unexpected: %q", string(skillBytes))
	}

	// Fresh git history present.
	if _, err := gogit.PlainOpen(canonical); err != nil {
		t.Errorf("expected git repo at %s: %v", canonical, err)
	}

	// Entry mutations.
	if entry.Mode != model.ModeEdit {
		t.Errorf("entry.Mode = %q, want %q", entry.Mode, model.ModeEdit)
	}
	if entry.EditPath != ".claude/skills/demo" {
		t.Errorf("entry.EditPath = %q, want %q", entry.EditPath, ".claude/skills/demo")
	}
	if entry.SourceUpstream != originalSource {
		t.Errorf("entry.SourceUpstream = %q, want original Source %q", entry.SourceUpstream, originalSource)
	}
	if !entry.IsEdit() {
		t.Errorf("entry.IsEdit() = false, want true")
	}
}

// TestEjectToTarget_Idempotent verifies that running edit twice does nothing
// the second time. The wrapper command relies on this via the no-op path.
func TestEjectToTarget_Idempotent(t *testing.T) {
	entry := seedSharedWorktreeForEject(t, "demo", "raks")
	projectRoot := t.TempDir()

	if _, err := skill.EjectToTarget(skill.EjectRequest{Entry: entry, ProjectRoot: projectRoot}); err != nil {
		t.Fatalf("first eject: %v", err)
	}
	// Second call: entry already flipped, no on-disk mutation expected.
	result, err := skill.EjectToTarget(skill.EjectRequest{Entry: entry, ProjectRoot: projectRoot})
	if err != nil {
		t.Fatalf("second eject: %v", err)
	}
	if result.EditPath != ".claude/skills/demo" {
		t.Errorf("second eject EditPath = %q, want unchanged", result.EditPath)
	}
}

// TestEjectToTarget_MultiTargetSiblingSymlinks verifies that when the install
// covers multiple targets, the alphabetical-first one becomes the canonical
// real dir and the others become relative symlinks pointing at it. This is
// the "Option A" promise: one canonical, others follow.
func TestEjectToTarget_MultiTargetSiblingSymlinks(t *testing.T) {
	entry := seedSharedWorktreeForEject(t, "demo", "raks")
	entry.Targets = []string{"codex", "claude"} // intentionally unsorted

	projectRoot := t.TempDir()
	result, err := skill.EjectToTarget(skill.EjectRequest{Entry: entry, ProjectRoot: projectRoot})
	if err != nil {
		t.Fatalf("EjectToTarget: %v", err)
	}

	// claude wins alphabetical-first, so .claude/skills/demo is canonical.
	if result.CanonicalTarget != "claude" {
		t.Errorf("CanonicalTarget = %q, want claude", result.CanonicalTarget)
	}
	canonical := filepath.Join(projectRoot, ".claude", "skills", "demo")
	canonicalInfo, err := os.Lstat(canonical)
	if err != nil {
		t.Fatalf("lstat canonical: %v", err)
	}
	if canonicalInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("canonical %s is a symlink, expected real dir", canonical)
	}

	// codex should be a symlink pointing at the canonical (relative).
	sibling := filepath.Join(projectRoot, ".codex", "skills", "demo")
	siblingInfo, err := os.Lstat(sibling)
	if err != nil {
		t.Fatalf("lstat sibling: %v", err)
	}
	if siblingInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("sibling %s is not a symlink", sibling)
	}
	link, err := os.Readlink(sibling)
	if err != nil {
		t.Fatalf("readlink sibling: %v", err)
	}
	if filepath.IsAbs(link) {
		t.Errorf("sibling symlink %q is absolute, want relative for portability", link)
	}
	// Resolving the sibling should land on the canonical dir.
	resolved, err := filepath.EvalSymlinks(sibling)
	if err != nil {
		t.Fatalf("evalsymlinks sibling: %v", err)
	}
	canonicalReal, _ := filepath.EvalSymlinks(canonical)
	if resolved != canonicalReal {
		t.Errorf("sibling resolves to %s, want %s", resolved, canonicalReal)
	}

	if len(result.SiblingLinks) != 1 {
		t.Errorf("SiblingLinks = %v, want exactly 1 (codex)", result.SiblingLinks)
	}
}

// TestEjectToTarget_RefusesLink covers the "you wouldn't eject a link install"
// guard — link installs already live at an absolute path the user owns;
// re-ejecting them would be a footgun.
func TestEjectToTarget_RefusesLink(t *testing.T) {
	entry := &model.LockEntry{
		Name:    "demo",
		Source:  t.TempDir(),
		Ref:     "local",
		Targets: []string{"claude"},
	}
	_, err := skill.EjectToTarget(skill.EjectRequest{
		Entry:       entry,
		ProjectRoot: t.TempDir(),
	})
	if err == nil {
		t.Fatalf("expected error ejecting a link install, got nil")
	}
}

// TestEjectToTarget_VerifyCleanAfterEject is the regression guard for issue
// #80: `qvr edit` previously wrote a subtreeHash that immediately failed
// `qvr lock verify` because it included `.git/` bookkeeping (which changes
// out from under the hash). With .git/ excluded from HashSubtreeFromDisk
// and the Commit field re-sealed to the new edit-repo HEAD, eject leaves
// the entry in a state where `lock verify` reports no drift.
func TestEjectToTarget_VerifyCleanAfterEject(t *testing.T) {
	entry := seedSharedWorktreeForEject(t, "demo", "raks")
	projectRoot := t.TempDir()

	if _, err := skill.EjectToTarget(skill.EjectRequest{
		Entry:       entry,
		ProjectRoot: projectRoot,
	}); err != nil {
		t.Fatalf("EjectToTarget: %v", err)
	}

	res := skill.VerifySingleEntry(entry, projectRoot)
	if res.Status != skill.VerifyStatusOK {
		t.Errorf("expected verify status %q after eject, got %q (drift=%+v message=%q)",
			skill.VerifyStatusOK, res.Status, res.Drift, res.Message)
	}
}

// TestEjectToTarget_GlobalScopeLandsAtHome verifies issue #82 fix: when
// --global is set, eject writes EditPath as an absolute path under the
// user's home agent dir (~/.claude/skills/<name>), not as a project-relative
// path. Previously --global ejected to cwd and left the global lane
// untouched, leaving the global lockfile pointing at a cwd-dependent path.
func TestEjectToTarget_GlobalScopeLandsAtHome(t *testing.T) {
	// Pin HOME to a tempdir so the test can assert path layout without
	// touching the real ~/.claude.
	home := t.TempDir()
	t.Setenv("HOME", home)
	entry := seedSharedWorktreeForEject(t, "demo", "raks")
	projectRoot := t.TempDir()

	if _, err := skill.EjectToTarget(skill.EjectRequest{
		Entry:       entry,
		ProjectRoot: projectRoot,
		Global:      true,
	}); err != nil {
		t.Fatalf("EjectToTarget --global: %v", err)
	}

	if !filepath.IsAbs(entry.EditPath) {
		t.Errorf("EditPath = %q, want absolute path for --global eject", entry.EditPath)
	}
	wantPrefix := filepath.Join(home, ".claude/skills/demo")
	if entry.EditPath != wantPrefix {
		t.Errorf("EditPath = %q, want %q (under user home)", entry.EditPath, wantPrefix)
	}
	if _, err := os.Stat(filepath.Join(entry.EditPath, "SKILL.md")); err != nil {
		t.Errorf("eject dir not populated under home: %v", err)
	}
	// Project's .claude/skills/demo must NOT have been created.
	if _, err := os.Stat(filepath.Join(projectRoot, ".claude/skills/demo")); err == nil {
		t.Errorf("--global eject leaked into project (.claude/skills/demo exists in projectRoot)")
	}
}
