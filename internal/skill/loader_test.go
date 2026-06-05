package skill_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/quiver-cli/qvr/internal/skill"
)

func TestLoadFromPath_ValidSkill(t *testing.T) {
	// Use testdata fixture
	dir := filepath.Join("..", "..", "testdata", "valid-skill")
	s, err := skill.LoadFromPath(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if s.Frontmatter.Name != "valid-skill" {
		t.Errorf("name = %q, want %q", s.Frontmatter.Name, "valid-skill")
	}
	if s.Name != "valid-skill" {
		t.Errorf("dir name = %q, want %q", s.Name, "valid-skill")
	}
	if s.Frontmatter.Description == "" {
		t.Error("description should not be empty")
	}
	if s.Frontmatter.License != "MIT" {
		t.Errorf("license = %q, want %q", s.Frontmatter.License, "MIT")
	}

	// Check files were listed
	hasSkillMD := false
	hasScript := false
	hasRef := false
	for _, f := range s.Files {
		switch f {
		case "SKILL.md":
			hasSkillMD = true
		case "scripts/example.sh":
			hasScript = true
		case "references/NOTES.md":
			hasRef = true
		}
	}
	if !hasSkillMD {
		t.Error("files should include SKILL.md")
	}
	if !hasScript {
		t.Error("files should include scripts/example.sh")
	}
	if !hasRef {
		t.Error("files should include references/NOTES.md")
	}
}

func TestLoadFromPath_NoSkillMD(t *testing.T) {
	dir := t.TempDir()
	_, err := skill.LoadFromPath(dir)
	if err == nil {
		t.Error("expected error for directory without SKILL.md")
	}
}

func TestLoadFromPath_NotADirectory(t *testing.T) {
	f, err := os.CreateTemp("", "test-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())

	_, err = skill.LoadFromPath(f.Name())
	if err == nil {
		t.Error("expected error for non-directory path")
	}
}

func TestLoadFromPath_BadFrontmatter(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "invalid-skill-bad-format")
	_, err := skill.LoadFromPath(dir)
	if err == nil {
		t.Error("expected error for bad frontmatter")
	}
}

func TestLoadFromPath_NonexistentPath(t *testing.T) {
	_, err := skill.LoadFromPath("/nonexistent/path/to/skill")
	if err == nil {
		t.Error("expected error for nonexistent path")
	}
}
