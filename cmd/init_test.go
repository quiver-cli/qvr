package cmd

import (
	"os"
	"path/filepath"
	"testing"

	gogit "github.com/go-git/go-git/v5"

	"github.com/quiver-cli/qvr/internal/config"
	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/output"
)

// TestRunInit_StandaloneCreatesGitRepo pins #150 at the init side: a
// standalone-scaffolded skill must be a real git repo with an initial commit,
// so the `qvr publish ./<name> --fork <url>` flow the success message
// advertises round-trips with no manual git plumbing.
func TestRunInit_StandaloneCreatesGitRepo(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	project := t.TempDir()
	t.Chdir(project)
	withCapturingPrinter(t, output.FormatText)

	t.Cleanup(func() { initStandalone = false; initType = "simple" })
	initStandalone = true
	initType = "simple"

	if err := runInit(initCmd, []string{"demo"}); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	dir := filepath.Join(project, "demo")
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("init dir is not a git repo (#150): %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("no HEAD commit after init: %v", err)
	}
	if head.Hash().IsZero() {
		t.Error("HEAD is zero — init did not commit the scaffold")
	}
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Errorf("SKILL.md missing: %v", err)
	}
}

// TestRunInit_ProjectScopedCreatesGitRepo is the same guard for the default
// project-scoped path: the edit dir must be a repo from birth, like a
// worktree-ejected edit dir.
func TestRunInit_ProjectScopedCreatesGitRepo(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	project := t.TempDir()
	t.Chdir(project)
	withCapturingPrinter(t, output.FormatText)

	t.Cleanup(func() {
		initStandalone = false
		initType = "simple"
		initTarget = "claude"
		initGlobal = false
	})
	initStandalone = false
	initType = "simple"
	initTarget = "claude"

	if err := runInit(initCmd, []string{"demo"}); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	editDir := filepath.Join(project, ".claude", "skills", "demo")
	if _, err := gogit.PlainOpen(editDir); err != nil {
		t.Fatalf("project-scoped edit dir is not a git repo (#150): %v", err)
	}
	// The lock entry should be mode:edit pointing at it.
	lock, err := model.ReadLockFile(filepath.Join(project, model.LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get("demo")
	if err != nil {
		t.Fatalf("lock get: %v", err)
	}
	if entry.Mode != model.ModeEdit {
		t.Errorf("entry.Mode = %q, want edit", entry.Mode)
	}
}
