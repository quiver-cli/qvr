package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeReadTestSkill(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "---\nname: " + name + "\ndescription: test\n---\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	return dir
}

func linkSkillInto(t *testing.T, projectRoot, target, name, src string) {
	t.Helper()
	linkParent := filepath.Join(projectRoot, target)
	if err := os.MkdirAll(linkParent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	if err := os.Symlink(src, filepath.Join(linkParent, name)); err != nil {
		t.Fatalf("symlink: %v", err)
	}
}

func TestReadSkillFile_Found(t *testing.T) {
	project := t.TempDir()
	src := writeReadTestSkill(t, "demo")
	linkSkillInto(t, project, ".claude/skills", "demo", src)

	data, sawSkill, fileErr := readSkillFile("demo", "SKILL.md", project, []string{"claude"})
	if !sawSkill {
		t.Fatalf("sawSkill should be true")
	}
	if fileErr != nil {
		t.Fatalf("unexpected fileErr: %v", fileErr)
	}
	if !strings.Contains(string(data), "# demo") {
		t.Errorf("unexpected content: %q", data)
	}
}

func TestReadSkillFile_FileMissing_ReportsFileNotSkill(t *testing.T) {
	project := t.TempDir()
	src := writeReadTestSkill(t, "demo")
	linkSkillInto(t, project, ".claude/skills", "demo", src)

	data, sawSkill, fileErr := readSkillFile(
		"demo", "references/missing.md", project,
		[]string{"claude", "cursor"},
	)
	if data != nil {
		t.Fatalf("expected nil data, got %q", data)
	}
	if !sawSkill {
		t.Fatalf("sawSkill should be true — symlink existed even though file did not")
	}
	if fileErr == nil {
		t.Fatal("expected a fileErr describing the missing file")
	}
	msg := fileErr.Error()
	for _, want := range []string{`"references/missing.md"`, `"demo"`, "not found"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
	if strings.Contains(msg, "skill \"demo\" not found in any agent target") {
		t.Errorf("error should not blame the skill: %q", msg)
	}
}

func TestReadSkillFile_SkillMissing(t *testing.T) {
	project := t.TempDir()
	data, sawSkill, fileErr := readSkillFile(
		"ghost", "SKILL.md", project,
		[]string{"claude", "cursor", "copilot"},
	)
	if data != nil {
		t.Fatalf("expected nil data")
	}
	if sawSkill {
		t.Fatalf("sawSkill should be false when no target has a symlink")
	}
	if fileErr != nil {
		t.Fatalf("fileErr should be nil when no target had the skill, got %v", fileErr)
	}
}

func TestReadSkillFile_PathTraversalRefused(t *testing.T) {
	project := t.TempDir()
	src := writeReadTestSkill(t, "demo")
	linkSkillInto(t, project, ".claude/skills", "demo", src)

	data, sawSkill, fileErr := readSkillFile("demo", "../escape", project, []string{"claude"})
	if data != nil {
		t.Fatalf("traversal should not read: got %q", data)
	}
	if !sawSkill {
		t.Fatalf("symlink existed; sawSkill should be true")
	}
	if fileErr == nil {
		t.Fatal("expected an error rejecting the traversal")
	}
	if !strings.Contains(fileErr.Error(), "escapes") {
		t.Errorf("error should mention traversal/escape, got %q", fileErr.Error())
	}
}

func TestOrderedTargets_Default(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	got := orderedTargets()
	if len(got) == 0 || got[0] != "claude" {
		t.Errorf("expected canonical order with claude first, got %v", got)
	}
	if len(got) != 6 {
		t.Errorf("expected 6 targets, got %d", len(got))
	}
}
