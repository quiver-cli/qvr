package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raks097/quiver/internal/model"
)

func writeFullSkill(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o755); err != nil {
		t.Fatalf("mkdir references: %v", err)
	}
	body := "---\n" +
		"name: " + name + "\n" +
		"description: detailed test skill\n" +
		"license: MIT\n" +
		"metadata:\n" +
		"  author: test-org\n" +
		"  tags: deploy,demo\n" +
		"---\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "run.sh"), []byte("echo hi"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "references", "spec.md"), []byte("ref"), 0o644); err != nil {
		t.Fatalf("write ref: %v", err)
	}
	return dir
}

func TestBuildSkillInfo_FullSkill(t *testing.T) {
	wt := writeFullSkill(t, "demo")
	project := t.TempDir()
	linkSkillInto(t, project, ".claude/skills", "demo", wt)

	entry := &model.LockEntry{
		Name:     "demo",
		Registry: "acme",
		Branch:   "main",
		Commit:   "deadbeef",
		Worktree: wt,
		Targets:  []string{"claude"},
	}

	info, err := buildSkillInfo(entry, project)
	if err != nil {
		t.Fatalf("buildSkillInfo: %v", err)
	}
	if info.Name != "demo" || info.Description != "detailed test skill" {
		t.Errorf("frontmatter not propagated: %+v", info)
	}
	if info.License != "MIT" {
		t.Errorf("license = %q, want MIT", info.License)
	}
	if info.Metadata["author"] != "test-org" || info.Metadata["tags"] != "deploy,demo" {
		t.Errorf("metadata not propagated: %v", info.Metadata)
	}
	wantFiles := []string{"SKILL.md", "references/spec.md", "scripts/run.sh"}
	gotFiles := strings.Join(info.Files, ",")
	for _, want := range wantFiles {
		if !strings.Contains(gotFiles, want) {
			t.Errorf("expected %q in files, got %v", want, info.Files)
		}
	}
	if len(info.Targets) != 1 || info.Targets[0].Target != "claude" || !info.Targets[0].OK {
		t.Errorf("expected one OK target for claude, got %+v", info.Targets)
	}
}

func TestBuildSkillInfo_BrokenSymlinkReportsError(t *testing.T) {
	wt := writeFullSkill(t, "demo")
	project := t.TempDir()

	otherSrc := writeFullSkill(t, "demo")
	linkSkillInto(t, project, ".claude/skills", "demo", otherSrc)

	entry := &model.LockEntry{
		Name:     "demo",
		Worktree: wt,
		Targets:  []string{"claude"},
	}
	info, err := buildSkillInfo(entry, project)
	if err != nil {
		t.Fatalf("buildSkillInfo: %v", err)
	}
	if len(info.Targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(info.Targets))
	}
	if info.Targets[0].OK {
		t.Errorf("symlink mismatch should not be OK: %+v", info.Targets[0])
	}
	if info.Targets[0].Error == "" {
		t.Errorf("expected an error message, got empty string")
	}
}

// Mirrors the real layout of a registry-installed skill: a bare worktree root
// with SKILL.md living under a `skills/<name>/` sub-path. Issue #16: info was
// calling LoadFromPath(worktree) instead of joining entry.Path, so frontmatter
// came back empty for every multi-skill registry.
func TestBuildSkillInfo_LoadsFrontmatterFromSkillPath(t *testing.T) {
	worktree := t.TempDir()
	skillRel := filepath.Join("skills", "deploy-to-vercel")
	skillDir := filepath.Join(worktree, skillRel)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "---\nname: deploy-to-vercel\ndescription: Deploy to Vercel\n---\n# deploy\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	entry := &model.LockEntry{
		Name:     "deploy-to-vercel",
		Registry: "vercel",
		Branch:   "main",
		Worktree: worktree,
		Path:     skillRel,
		Targets:  []string{"claude"},
	}
	info, err := buildSkillInfo(entry, t.TempDir())
	if err != nil {
		t.Fatalf("buildSkillInfo: %v", err)
	}
	if info.Description != "Deploy to Vercel" {
		t.Errorf("description = %q, want %q", info.Description, "Deploy to Vercel")
	}
}

// Linked skills have no worktree/branch/commit; info should carry LinkTarget
// and the render path should suppress the empty git-state rows rather than
// printing blank columns.
func TestBuildSkillInfo_LinkedSkill(t *testing.T) {
	src := writeFullSkill(t, "demo")
	project := t.TempDir()
	linkSkillInto(t, project, ".claude/skills", "demo", src)

	entry := &model.LockEntry{
		Name:       "demo",
		Source:     "link",
		LinkTarget: src,
		Path:       src,
		Targets:    []string{"claude"},
	}
	info, err := buildSkillInfo(entry, project)
	if err != nil {
		t.Fatalf("buildSkillInfo: %v", err)
	}
	if info.LinkTarget != src {
		t.Errorf("LinkTarget = %q, want %q", info.LinkTarget, src)
	}
	if info.Branch != "" || info.Commit != "" || info.Worktree != "" {
		t.Errorf("link entry should have empty git state, got %+v", info)
	}
	if info.Description != "detailed test skill" {
		t.Errorf("description not loaded from link target: %q", info.Description)
	}
}

func TestBuildSkillInfo_TargetWithNoSymlinkReportsError(t *testing.T) {
	wt := writeFullSkill(t, "demo")
	project := t.TempDir()
	entry := &model.LockEntry{
		Name:     "demo",
		Worktree: wt,
		Targets:  []string{"claude", "cursor"},
	}
	info, err := buildSkillInfo(entry, project)
	if err != nil {
		t.Fatalf("buildSkillInfo: %v", err)
	}
	if len(info.Targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(info.Targets))
	}
	for _, ts := range info.Targets {
		if ts.OK {
			t.Errorf("no symlinks were created; %s should not be OK", ts.Target)
		}
		if ts.Error == "" {
			t.Errorf("expected error for %s", ts.Target)
		}
	}
}
