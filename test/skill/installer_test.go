package skilltests

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raks097/quiver/internal/registry"
	"github.com/raks097/quiver/internal/skill"
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
	expectedWt := registry.WorktreePath("acme", "code-review", "main")
	if _, err := os.Stat(filepath.Join(expectedWt, "skills", "code-review", "SKILL.md")); err != nil {
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
	if _, err := os.Stat(filepath.Join(expectedWt, "skills", "deploy-helper")); !os.IsNotExist(err) {
		t.Errorf("sparse should have removed deploy-helper: %v", err)
	}

	// Lock file records install.
	lockPath := filepath.Join(h.project, "qvr.lock.json")
	if _, err := os.Stat(lockPath); err != nil {
		t.Errorf("lock file missing: %v", err)
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
	expectedWt := registry.WorktreePath("acme", "code-review", "v2")
	if _, err := os.Stat(expectedWt); err != nil {
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

	err := h.installer.Remove("code-review", skill.InstallRequest{ProjectRoot: h.project})
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	// Symlink and worktree both gone.
	if _, err := os.Lstat(filepath.Join(h.project, ".claude/skills/code-review")); !os.IsNotExist(err) {
		t.Errorf("symlink survived: %v", err)
	}
	wt := registry.WorktreePath("acme", "code-review", "main")
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree survived: %v", err)
	}
}

func TestRestoreAll(t *testing.T) {
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

	// Blow away the worktree + symlinks (simulate fresh checkout).
	_ = os.RemoveAll(registry.WorktreesRoot())
	_ = os.RemoveAll(filepath.Join(h.project, ".claude"))

	results, err := h.installer.RestoreAll(skill.InstallRequest{ProjectRoot: h.project})
	if err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 restored, got %d", len(results))
	}
	if _, err := os.Lstat(filepath.Join(h.project, ".claude/skills/code-review")); err != nil {
		t.Errorf("symlink not restored: %v", err)
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
	lockPath := filepath.Join(h.project, "qvr.lock.json")
	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	var lf struct {
		Skills map[string]struct {
			Source     string `json:"source"`
			LinkTarget string `json:"linkTarget"`
		} `json:"skills"`
	}
	if err := json.Unmarshal(data, &lf); err != nil {
		t.Fatalf("parse lock: %v", err)
	}
	entry, ok := lf.Skills["my-skill"]
	if !ok {
		t.Fatal("my-skill missing from lock")
	}
	if entry.Source != "link" || entry.LinkTarget != absLocal {
		t.Errorf("lock entry = %+v, want source=link, target=%s", entry, absLocal)
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
