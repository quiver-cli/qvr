package skill_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/registry"
	"github.com/quiver-cli/qvr/internal/skill"
)

func TestInstall_BasicFlow(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{
		"code-review":   codeReviewSkill,
		"deploy-helper": deployHelperSkill,
	})
	h.addRegistry(t, "acme", remote)

	result, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review",
		Targets:     []string{"claude", "cursor"},
		ProjectRoot: h.project,
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if result.Name != "code-review" {
		t.Errorf("name = %s", result.Name)
	}
	if result.Version != "main" {
		t.Errorf("version = %s, want main", result.Version)
	}

	// Worktree exists with the skill dir.
	if _, err := os.Stat(filepath.Join(result.Worktree, "skills", "code-review", "SKILL.md")); err != nil {
		t.Errorf("worktree skill missing: %v", err)
	}

	// Symlinks exist for both targets.
	for _, target := range []string{".claude/skills", ".cursor/rules"} {
		linkPath := filepath.Join(h.project, target, "code-review")
		info, err := os.Lstat(linkPath)
		if err != nil {
			t.Errorf("link missing for %s: %v", target, err)
			continue
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s is not a symlink", linkPath)
		}
	}

	// Sparse checkout trimmed deploy-helper.
	if _, err := os.Stat(filepath.Join(result.Worktree, "skills", "deploy-helper")); !os.IsNotExist(err) {
		t.Errorf("sparse should have removed deploy-helper: %v", err)
	}

	// Lock file records install.
	lockPath := filepath.Join(h.project, model.LockFileName)
	if _, err := os.Stat(lockPath); err != nil {
		t.Errorf("lock file missing: %v", err)
	}
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get("code-review")
	if err != nil {
		t.Fatalf("lock get: %v", err)
	}
	if entry.CommitAuthor != "Test <t@t>" {
		t.Errorf("commitAuthor = %q, want Test <t@t>", entry.CommitAuthor)
	}
}

func TestInstall_RequireSignedRejectsUnsigned(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{
		"code-review": codeReviewSkill,
	})
	h.addRegistry(t, "acme", remote)

	_, err := h.installer.Install(skill.InstallRequest{
		Skill:         "code-review",
		Targets:       []string{"claude"},
		ProjectRoot:   h.project,
		RequireSigned: true,
	})
	if !errors.Is(err, skill.ErrSignatureRequired) {
		t.Fatalf("Install err = %v, want ErrSignatureRequired", err)
	}
}

func TestInstall_TrustedAuthorsRejectsMismatch(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{
		"code-review": codeReviewSkill,
	})
	h.addRegistry(t, "acme", remote)

	_, err := h.installer.Install(skill.InstallRequest{
		Skill:          "code-review",
		Targets:        []string{"claude"},
		ProjectRoot:    h.project,
		TrustedAuthors: []string{"Alice <alice@example.com>"},
	})
	if err == nil || !strings.Contains(err.Error(), "untrusted commit author") {
		t.Fatalf("Install err = %v, want untrusted commit author", err)
	}
}

func TestInstall_AddsNewTargetsWithoutRebuilding(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)

	if _, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	}); err != nil {
		t.Fatalf("first install: %v", err)
	}
	result, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review",
		Targets:     []string{"cursor"},
		ProjectRoot: h.project,
	})
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if len(result.Targets) != 2 {
		t.Errorf("expected merged targets [claude cursor], got %v", result.Targets)
	}
}

func TestInstall_UnknownTarget(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)
	_, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review",
		Targets:     []string{"nonexistent"},
		ProjectRoot: h.project,
	})
	if !errors.Is(err, skill.ErrUnknownTarget) {
		t.Errorf("expected ErrUnknownTarget, got %v", err)
	}
}

func TestInstall_UnknownSkill(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)
	_, err := h.installer.Install(skill.InstallRequest{
		Skill:       "no-such-skill",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	})
	if !errors.Is(err, skill.ErrSkillNotFound) {
		t.Errorf("expected ErrSkillNotFound, got %v", err)
	}
}

func TestInstall_AtVersion(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill}, "v2")
	h.addRegistry(t, "acme", remote)

	result, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review@v2",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	})
	if err != nil {
		t.Fatalf("install @v2: %v", err)
	}
	if result.Version != "v2" {
		t.Errorf("version = %s, want v2", result.Version)
	}
	if _, err := os.Stat(result.Worktree); err != nil {
		t.Errorf("worktree at v2 missing: %v", err)
	}
}

func TestInstall_AtomicOnBadRef(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)

	_, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review@nope",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	})
	if err == nil {
		t.Fatal("expected failure on missing ref")
	}
	// No staging, no final dir, no broken symlink should remain.
	// The worktree path is now SHA-keyed; for a bad ref, ResolveRef fails
	// and falls back to the ref label itself, so the would-be path is
	// computable for the leak check.
	finalPath := registry.WorktreePath("acme", "code-review", "nope")
	if _, err := os.Stat(finalPath); !os.IsNotExist(err) {
		t.Errorf("finalPath leaked: %v", err)
	}
	if _, err := os.Stat(finalPath + ".staging"); !os.IsNotExist(err) {
		t.Errorf("staging path leaked: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(h.project, ".claude/skills/code-review")); !os.IsNotExist(err) {
		t.Errorf("symlink leaked: %v", err)
	}
}

func TestInstall_InvalidSkillRejects(t *testing.T) {
	h := newHarness(t)
	// Consecutive hyphens in the name violate the spec. Directory name
	// matches frontmatter, so FindSkill succeeds — but the validator must
	// refuse the install at checkout time.
	remote := seedRemote(t, map[string]string{
		"bad--skill": `---
name: bad--skill
description: has consecutive hyphens
---
# bad
`,
	})
	h.addRegistry(t, "acme", remote)

	_, err := h.installer.Install(skill.InstallRequest{
		Skill:       "bad--skill",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	})
	if err == nil || !strings.Contains(err.Error(), "validation failed") {
		t.Errorf("expected validation failure, got %v", err)
	}
}

func TestRemove(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)
	if _, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}

	// Capture the worktree path from the lock before removing.
	lock, _ := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	preWt := ""
	if e, err := lock.Get("code-review"); err == nil {
		preWt = skill.EntryWorktreePath(e)
	}

	err := h.installer.Remove("code-review", skill.InstallRequest{ProjectRoot: h.project})
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	// Symlink and worktree both gone.
	if _, err := os.Lstat(filepath.Join(h.project, ".claude/skills/code-review")); !os.IsNotExist(err) {
		t.Errorf("symlink survived: %v", err)
	}
	if preWt != "" {
		if _, err := os.Stat(preWt); !os.IsNotExist(err) {
			t.Errorf("worktree survived: %v", err)
		}
	}
}

// TestRemove_RefusesEditWithoutForce is the regression guard for issue
// #93: removing a mode:edit skill without --force must error AND must
// leave the lock entry intact (no orphan state). Previously the lockfile
// entry was dropped before the FS step ran; on the FS failure the user
// was left with the eject dir on disk but no lock entry to drive recovery.
func TestRemove_RefusesEditWithoutForce(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)
	if _, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	// Eject so the entry is in mode:edit.
	lock, _ := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	entry, _ := lock.Get("code-review")
	if _, err := skill.EjectToTarget(skill.EjectRequest{Entry: entry, ProjectRoot: h.project}); err != nil {
		t.Fatalf("eject: %v", err)
	}
	lock.Put(entry)
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock after eject: %v", err)
	}

	// Remove without --force should error and leave the lock entry intact.
	err := h.installer.Remove("code-review", skill.InstallRequest{ProjectRoot: h.project, Force: false})
	if err == nil {
		t.Fatal("expected error removing mode:edit skill without --force, got nil")
	}
	reloaded, _ := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	if _, gerr := reloaded.Get("code-review"); gerr != nil {
		t.Errorf("lock entry was dropped despite remove failing: %v (regression of #93)", gerr)
	}
	// Eject dir must still be on disk.
	if _, err := os.Stat(filepath.Join(h.project, ".claude/skills/code-review/SKILL.md")); err != nil {
		t.Errorf("eject dir removed even though remove errored: %v", err)
	}
}

// TestRemove_ForceDeletesEditDir verifies that `qvr remove --force` on a
// mode:edit skill rm -rf's the eject dir and clears the lock entry — the
// happy path users opt into when they're sure they want to discard the
// ejected edits.
func TestRemove_ForceDeletesEditDir(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)
	if _, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	lock, _ := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	entry, _ := lock.Get("code-review")
	if _, err := skill.EjectToTarget(skill.EjectRequest{Entry: entry, ProjectRoot: h.project}); err != nil {
		t.Fatalf("eject: %v", err)
	}
	lock.Put(entry)
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock after eject: %v", err)
	}

	if err := h.installer.Remove("code-review", skill.InstallRequest{ProjectRoot: h.project, Force: true}); err != nil {
		t.Fatalf("Remove --force: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.project, ".claude/skills/code-review")); !os.IsNotExist(err) {
		t.Errorf("eject dir survived --force remove: %v", err)
	}
	reloaded, _ := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	if _, gerr := reloaded.Get("code-review"); gerr == nil {
		t.Error("lock entry still present after Remove --force")
	}
}

func TestLink(t *testing.T) {
	h := newHarness(t)

	// Create a local skill.
	local := filepath.Join(t.TempDir(), "my-skill")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := `---
name: my-skill
description: local dev skill
---
# local
`
	if err := os.WriteFile(filepath.Join(local, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	result, err := h.installer.Link(local, skill.InstallRequest{
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	})
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	if result.Name != "my-skill" {
		t.Errorf("name = %s", result.Name)
	}
	linkPath := filepath.Join(h.project, ".claude/skills/my-skill")
	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	absLocal, _ := filepath.Abs(local)
	if target != absLocal {
		t.Errorf("target = %s, want %s", target, absLocal)
	}

	// Lock file records "link" source.
	lockPath := filepath.Join(h.project, model.LockFileName)
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get("my-skill")
	if err != nil {
		t.Fatalf("my-skill missing from lock: %v", err)
	}
	// v5: link installs carry the absolute path in Source and Ref="local".
	if entry.Source != absLocal {
		t.Errorf("lock entry source = %q, want %s", entry.Source, absLocal)
	}
	if entry.Ref != "local" {
		t.Errorf("lock entry ref = %q, want local", entry.Ref)
	}
	// #133 / AC-LIFE-9: a link install must serialise mode:"link" so the lock
	// is self-describing; IsLink() also keys off Ref=="local", but the §4
	// schema requires the explicit field.
	if entry.Mode != model.ModeLink {
		t.Errorf("lock entry mode = %q, want %q", entry.Mode, model.ModeLink)
	}
}

// Regression for the v0.3.6 punch list: `qvr link` used to accept a directory
// whose name didn't match the frontmatter `name`, producing a worktree that
// `qvr validate` / `qvr doctor` immediately flagged as broken. Link should
// apply the same name-matches-directory check the validator does.
func TestLink_RejectsDirNameMismatch(t *testing.T) {
	h := newHarness(t)

	local := filepath.Join(t.TempDir(), "wrong-dir-name")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := `---
name: my-skill
description: local dev skill
---
# local
`
	if err := os.WriteFile(filepath.Join(local, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := h.installer.Link(local, skill.InstallRequest{
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	})
	if err == nil {
		t.Fatal("expected link to reject name/dir mismatch")
	}
	if !strings.Contains(err.Error(), "must match directory name") {
		t.Errorf("error = %q, want mention of directory-name mismatch", err.Error())
	}
}

// TestInstall_FrozenNoLock_RequiresReadableLock is the #132 / AC-FROZEN-2
// guard: `--frozen` with no lock file at all must fail with the contract
// string "requires a readable lock file", not the downstream "skill not
// present in lock file" (which only applies when a lock exists but lacks the
// entry). ReadLockFile treats a missing file as an empty lock, so without the
// explicit existence check the frozen path slid into the wrong error.
func TestInstall_FrozenNoLock_RequiresReadableLock(t *testing.T) {
	h := newHarness(t)
	lockPath := filepath.Join(h.project, model.LockFileName)
	if _, err := os.Stat(lockPath); err == nil {
		t.Fatalf("precondition: lock file already exists at %s", lockPath)
	}

	_, err := h.installer.Install(skill.InstallRequest{
		Skill:       "demo@main",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
		Frozen:      true,
	})
	if err == nil {
		t.Fatal("expected --frozen with no lock to error")
	}
	if !strings.Contains(err.Error(), "--frozen requires a readable lock file") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "--frozen requires a readable lock file")
	}
	if strings.Contains(err.Error(), "not present in lock file") {
		t.Errorf("error leaked the wrong contract string (skill-not-present): %q", err.Error())
	}
}

func TestParseReference(t *testing.T) {
	cases := []struct {
		in      string
		name    string
		version string
		wantErr bool
	}{
		{"code-review", "code-review", "", false},
		{"code-review@v2", "code-review", "v2", false},
		{"code-review@", "code-review", "", false},
		{"", "", "", true},
		{"@v2", "", "", true},
	}
	for _, c := range cases {
		n, v, err := skill.ParseReference(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("ParseReference(%q) err=%v, wantErr=%v", c.in, err, c.wantErr)
		}
		if n != c.name || v != c.version {
			t.Errorf("ParseReference(%q) = (%q,%q), want (%q,%q)", c.in, n, v, c.name, c.version)
		}
	}
}
