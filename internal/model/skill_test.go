package model_test

import (
	"testing"

	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/pkg/skillspec"
)

func TestSkill_EmbedsFrontmatter(t *testing.T) {
	s := model.Skill{
		Skill: skillspec.Skill{
			Frontmatter: skillspec.Frontmatter{
				Name:        "test-skill",
				Description: "A test skill.",
			},
			Body: "# Test",
		},
		Dir:   "/tmp/test-skill",
		Name:  "test-skill",
		Files: []string{"SKILL.md"},
	}

	if s.Frontmatter.Name != "test-skill" {
		t.Errorf("name = %q, want %q", s.Frontmatter.Name, "test-skill")
	}
	if s.Dir != "/tmp/test-skill" {
		t.Errorf("dir = %q, want %q", s.Dir, "/tmp/test-skill")
	}
	if len(s.Files) != 1 {
		t.Errorf("files len = %d, want 1", len(s.Files))
	}
}

func TestTargetNames(t *testing.T) {
	names := model.TargetNames()
	if len(names) != 6 {
		t.Errorf("expected 6 targets, got %d", len(names))
	}
	// Check sorted
	for i := 1; i < len(names); i++ {
		if names[i] < names[i-1] {
			t.Errorf("target names not sorted: %v", names)
			break
		}
	}
}

func TestTargets_AllHaveDirs(t *testing.T) {
	for name, target := range model.Targets {
		if target.Name == "" {
			t.Errorf("target %q has empty display name", name)
		}
		if target.LocalDir == "" {
			t.Errorf("target %q has empty local dir", name)
		}
		if target.GlobalDir == "" {
			t.Errorf("target %q has empty global dir", name)
		}
	}
}
