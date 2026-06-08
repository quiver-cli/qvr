package model_test

import (
	"testing"

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/pkg/skillspec"
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
	// The data-driven registry ships many agents; assert it's populated and
	// that the long-standing core targets are present, rather than pinning an
	// exact count that churns every time an agent is added.
	if len(names) < 6 {
		t.Errorf("expected at least the 6 core targets, got %d", len(names))
	}
	got := make(map[string]struct{}, len(names))
	for _, n := range names {
		got[n] = struct{}{}
	}
	for _, core := range []string{"claude", "codex", "copilot", "cursor", "project", "windsurf"} {
		if _, ok := got[core]; !ok {
			t.Errorf("core target %q missing from registry", core)
		}
	}
	// Check sorted
	for i := 1; i < len(names); i++ {
		if names[i] < names[i-1] {
			t.Errorf("target names not sorted: %v", names)
			break
		}
	}
}

func TestLookupTargetResolvesAliases(t *testing.T) {
	cases := map[string]string{
		"claude-code": "claude",
		"kilo":        "kilocode",
		"gemini-cli":  "gemini",
		"agents":      "project",
	}
	for alias, canonical := range cases {
		tgt, ok := model.LookupTarget(alias)
		if !ok {
			t.Errorf("alias %q did not resolve", alias)
			continue
		}
		if tgt.Name != canonical {
			t.Errorf("alias %q resolved to %q, want %q", alias, tgt.Name, canonical)
		}
		if c, ok := model.CanonicalTarget(alias); !ok || c != canonical {
			t.Errorf("CanonicalTarget(%q) = %q,%v want %q", alias, c, ok, canonical)
		}
	}
	if _, ok := model.LookupTarget("definitely-not-an-agent"); ok {
		t.Error("unknown target unexpectedly resolved")
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
