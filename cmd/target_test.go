package cmd

import (
	"os"
	"testing"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/model"
)

// targetTestSetup gives each test an isolated quiver home + project cwd and a
// clean printer, and resets the package-level flags the target/add commands
// read so tests don't leak state into one another.
func targetTestSetup(t *testing.T) string {
	t.Helper()
	t.Setenv("QUIVER_HOME", t.TempDir())
	project := t.TempDir()
	t.Chdir(project)
	resetPrinter(t)
	targetGlobal = false
	addTargets = nil
	t.Cleanup(func() { targetGlobal = false; addTargets = nil })
	return project
}

func projectFile(t *testing.T) *model.ProjectFile {
	t.Helper()
	project, _ := os.Getwd()
	proj, err := model.ReadProjectFile(model.DefaultProjectPath(project))
	if err != nil {
		t.Fatalf("read project file: %v", err)
	}
	return proj
}

func TestTargetAdd_WritesCanonicalSortedDefaults(t *testing.T) {
	targetTestSetup(t)

	// Aliases ("claude-code") normalise to canonical ("claude"); result sorted.
	if err := runTargetAdd(targetAddCmd, []string{"codex", "claude-code"}); err != nil {
		t.Fatalf("target add: %v", err)
	}
	got := projectFile(t).Project.DefaultTargets
	if len(got) != 2 || got[0] != "claude" || got[1] != "codex" {
		t.Fatalf("DefaultTargets = %v, want [claude codex]", got)
	}

	// Adding more unions in; re-adding an existing one doesn't duplicate.
	if err := runTargetAdd(targetAddCmd, []string{"gemini", "codex"}); err != nil {
		t.Fatalf("target add 2: %v", err)
	}
	got = projectFile(t).Project.DefaultTargets
	if len(got) != 3 || got[0] != "claude" || got[1] != "codex" || got[2] != "gemini" {
		t.Fatalf("DefaultTargets = %v, want [claude codex gemini]", got)
	}
}

func TestTargetAdd_RejectsUnknownAgent(t *testing.T) {
	targetTestSetup(t)
	err := runTargetAdd(targetAddCmd, []string{"not-a-real-agent"})
	if err == nil {
		t.Fatal("expected error for unknown agent")
	}
	// The unknown name must not be partially persisted.
	if got := projectFile(t).Project.DefaultTargets; len(got) != 0 {
		t.Errorf("DefaultTargets = %v, want empty after rejected add", got)
	}
}

func TestTarget_GlobalUnsupported(t *testing.T) {
	targetTestSetup(t)
	targetGlobal = true
	if err := runTargetAdd(targetAddCmd, []string{"claude"}); err == nil {
		t.Fatal("expected --global to be rejected for qvr target")
	}
}

func TestTargetRemove_DropsDefaults(t *testing.T) {
	targetTestSetup(t)
	if err := runTargetAdd(targetAddCmd, []string{"claude", "codex"}); err != nil {
		t.Fatalf("seed add: %v", err)
	}
	if err := runTargetRemove(targetRemoveCmd, []string{"codex"}); err != nil {
		t.Fatalf("target remove: %v", err)
	}
	got := projectFile(t).Project.DefaultTargets
	if len(got) != 1 || got[0] != "claude" {
		t.Fatalf("DefaultTargets = %v, want [claude]", got)
	}
}

// TestResolveAddTargets_Precedence verifies the strict, mutually-exclusive
// order: --target flag > qvr.toml default-targets > config default_target.
func TestResolveAddTargets_Precedence(t *testing.T) {
	project := targetTestSetup(t)
	cfg := &config.Config{DefaultTarget: "windsurf"}

	// 3) config fallback when nothing else set.
	got, err := resolveAddTargets(cfg, project)
	if err != nil {
		t.Fatalf("resolve (config): %v", err)
	}
	if len(got) != 1 || got[0] != "windsurf" {
		t.Fatalf("config fallback = %v, want [windsurf]", got)
	}

	// 2) qvr.toml defaults override config.
	if err := runTargetAdd(targetAddCmd, []string{"claude", "codex"}); err != nil {
		t.Fatalf("seed project defaults: %v", err)
	}
	got, err = resolveAddTargets(cfg, project)
	if err != nil {
		t.Fatalf("resolve (qvr.toml): %v", err)
	}
	if len(got) != 2 || got[0] != "claude" || got[1] != "codex" {
		t.Fatalf("qvr.toml defaults = %v, want [claude codex]", got)
	}

	// 1) explicit --target flag overrides everything, aliases normalised.
	addTargets = []string{"gemini-cli"}
	got, err = resolveAddTargets(cfg, project)
	if err != nil {
		t.Fatalf("resolve (flag): %v", err)
	}
	if len(got) != 1 || got[0] != "gemini" {
		t.Fatalf("flag override = %v, want [gemini]", got)
	}
}

func TestResolveAddTargets_NoneConfigured(t *testing.T) {
	project := targetTestSetup(t)
	if _, err := resolveAddTargets(&config.Config{}, project); err == nil {
		t.Fatal("expected error when no flag, no project defaults, no config default_target")
	}
}
