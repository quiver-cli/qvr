package model_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/astra-sh/qvr/internal/model"
)

func TestReadProjectFile_AbsentAndEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, model.ProjectFileName)

	// Absent → empty, not error (mirrors the lockfile's pre-first-install state).
	proj, err := model.ReadProjectFile(path)
	if err != nil {
		t.Fatalf("read absent: %v", err)
	}
	if len(proj.Skills) != 0 || len(proj.Project.DefaultTargets) != 0 {
		t.Fatalf("absent project file should be empty, got %+v", proj)
	}

	// Empty file → empty, not error.
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err = model.ReadProjectFile(path)
	if err != nil {
		t.Fatalf("read empty: %v", err)
	}
	if len(proj.Skills) != 0 {
		t.Fatalf("empty project file should parse to no skills, got %+v", proj.Skills)
	}
}

func TestProjectFile_RoundTrip_LosslessReservedSections(t *testing.T) {
	path := filepath.Join(t.TempDir(), model.ProjectFileName)

	// Hand-author a qvr.toml with a reserved [plugins] section to prove it
	// survives a qvr-managed write untouched (the basis of "additive milestones").
	raw := `[project]
name = "demo"
version = "1.2.3"
default-targets = ["claude", "codex"]

[skills]
"anthropics/skills/frontend-design" = "main"

[plugins]
"acme/bundle/web" = "v1.0.0"
`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	proj, err := model.ReadProjectFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if proj.Project.Name != "demo" || proj.Project.Version != "1.2.3" {
		t.Fatalf("project meta = %+v", proj.Project)
	}
	if proj.Skills["anthropics/skills/frontend-design"] != "main" {
		t.Fatalf("skills = %+v", proj.Skills)
	}
	if proj.Plugins == nil || proj.Plugins["acme/bundle/web"] != "v1.0.0" {
		t.Fatalf("reserved [plugins] not preserved: %+v", proj.Plugins)
	}

	// Mutate skills (a qvr-managed write) and confirm [plugins] still round-trips.
	proj.PutSkill("anthropics/skills/pdf", "v2")
	if err := proj.Write(); err != nil {
		t.Fatalf("write: %v", err)
	}
	reloaded, err := model.ReadProjectFile(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Plugins["acme/bundle/web"] != "v1.0.0" {
		t.Fatalf("reserved [plugins] lost after managed rewrite: %+v", reloaded.Plugins)
	}
	if reloaded.Skills["anthropics/skills/pdf"] != "v2" {
		t.Fatalf("new skill not persisted: %+v", reloaded.Skills)
	}
}

func TestMarshalProjectFile_Idempotent(t *testing.T) {
	proj := model.NewProjectFile("")
	proj.Project.Name = "x"
	proj.PutSkill("org/repo/a", "main")
	proj.PutSkill("org/repo/b", "v1")

	first, err := model.MarshalProjectFile(proj)
	if err != nil {
		t.Fatal(err)
	}
	second, err := model.MarshalProjectFile(proj)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("marshal not idempotent:\n%s\n---\n%s", first, second)
	}
	if first[len(first)-1] != '\n' {
		t.Fatal("marshal output must end in a newline")
	}
}

func TestProjectFile_DefaultTargets(t *testing.T) {
	p := model.NewProjectFile("")

	added := p.AddDefaultTargets("codex", "claude")
	if len(added) != 2 {
		t.Fatalf("first add returned %v, want 2", added)
	}
	if got := p.Project.DefaultTargets; len(got) != 2 || got[0] != "claude" || got[1] != "codex" {
		t.Fatalf("DefaultTargets = %v, want sorted [claude codex]", got)
	}
	if again := p.AddDefaultTargets("claude"); again != nil {
		t.Errorf("re-add reported %v, want nil", again)
	}
	removed := p.RemoveDefaultTargets("claude", "windsurf")
	if len(removed) != 1 || removed[0] != "claude" {
		t.Errorf("remove returned %v, want [claude]", removed)
	}
	if got := p.Project.DefaultTargets; len(got) != 1 || got[0] != "codex" {
		t.Errorf("DefaultTargets after remove = %v, want [codex]", got)
	}
}

func TestProjectFile_DefaultTargetsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), model.ProjectFileName)
	p := model.NewProjectFile(path)
	p.AddDefaultTargets("claude", "codex", "gemini")
	if err := p.Write(); err != nil {
		t.Fatalf("write: %v", err)
	}
	loaded, err := model.ReadProjectFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := []string{"claude", "codex", "gemini"}
	if got := loaded.Project.DefaultTargets; len(got) != len(want) {
		t.Fatalf("DefaultTargets = %v, want %v", got, want)
	}
	for i := range want {
		if loaded.Project.DefaultTargets[i] != want[i] {
			t.Fatalf("DefaultTargets = %v, want %v", loaded.Project.DefaultTargets, want)
		}
	}
}

func TestSkillCoordinate(t *testing.T) {
	tests := []struct {
		name  string
		entry *model.LockEntry
		want  string
	}{
		{"nil", nil, ""},
		{"shared registry-sourced", &model.LockEntry{Name: "frontend-design", Registry: "anthropics/skills", Mode: model.ModeShared}, "anthropics/skills/frontend-design"},
		{"aliased keeps local name", &model.LockEntry{Name: "fd-old", Canonical: "frontend-design", Registry: "anthropics/skills", Mode: model.ModeShared}, "anthropics/skills/fd-old"},
		{"edit mode → lock-only", &model.LockEntry{Name: "auth", Registry: "anthropics/skills", Mode: model.ModeEdit}, ""},
		{"link mode → lock-only", &model.LockEntry{Name: "auth", Registry: "anthropics/skills", Mode: model.ModeLink}, ""},
		{"local mode → lock-only", &model.LockEntry{Name: "auth", Registry: "anthropics/skills", Mode: model.ModeLocal}, ""},
		{"no registry → lock-only", &model.LockEntry{Name: "auth", Registry: "", Mode: model.ModeShared}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := model.SkillCoordinate(tc.entry); got != tc.want {
				t.Fatalf("SkillCoordinate = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestProjectFile_RemoveSkillIdempotent(t *testing.T) {
	p := model.NewProjectFile("")
	p.PutSkill("org/repo/a", "main")
	p.RemoveSkill("org/repo/a")
	p.RemoveSkill("org/repo/a") // second remove is a no-op, must not panic
	if _, err := p.GetSkill("org/repo/a"); err == nil {
		t.Fatal("expected skill to be gone")
	}
}
