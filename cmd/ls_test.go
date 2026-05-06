package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocateSkillDir_FindsViaSymlink(t *testing.T) {
	src := writeFullSkill(t, "demo")
	project := t.TempDir()
	linkSkillInto(t, project, ".cursor/rules", "demo", src)

	got, err := locateSkillDir("demo", project, []string{"claude", "cursor"})
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	want, err := filepath.EvalSymlinks(src)
	if err != nil {
		t.Fatalf("filepath.EvalSymlinks(%q) failed: %v", src, err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLocateSkillDir_Missing(t *testing.T) {
	_, err := locateSkillDir("ghost", t.TempDir(), []string{"claude"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestListSkillEntries_TopLevel(t *testing.T) {
	src := writeFullSkill(t, "demo")
	entries, err := listSkillEntries(src, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Path] = e.IsDir
	}
	for _, want := range []string{"SKILL.md", "scripts", "references"} {
		if _, ok := names[want]; !ok {
			t.Errorf("expected top-level entry %q in %v", want, names)
		}
	}
	if names["scripts"] != true {
		t.Errorf("scripts should be a directory")
	}
	if names["SKILL.md"] != false {
		t.Errorf("SKILL.md should be a file")
	}
	for path := range names {
		if path == "scripts/run.sh" {
			t.Errorf("non-recursive should not include nested files: got %q", path)
		}
	}
}

func TestListSkillEntries_Recursive(t *testing.T) {
	src := writeFullSkill(t, "demo")
	entries, err := listSkillEntries(src, true)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Path] = e.IsDir
	}
	for _, want := range []string{"SKILL.md", "scripts", "scripts/run.sh", "references", "references/spec.md"} {
		if _, ok := got[want]; !ok {
			t.Errorf("expected %q in recursive listing, got %v", want, got)
		}
	}
}

func TestListSkillEntries_RecursiveSkipsGitDir(t *testing.T) {
	src := writeFullSkill(t, "demo")
	gitDir := filepath.Join(src, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatalf("write HEAD: %v", err)
	}

	entries, err := listSkillEntries(src, true)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, e := range entries {
		if e.Path == ".git" || e.Path == ".git/HEAD" {
			t.Errorf("recursive listing leaked git metadata: %q", e.Path)
		}
	}
}
