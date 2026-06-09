package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	gogit "github.com/go-git/go-git/v5"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/output"
)

// TestRunCreate_StandaloneCreatesGitRepo pins #150 at the create side: a
// standalone-scaffolded skill must be a real git repo with an initial commit,
// so the `qvr publish ./<name> --fork <url>` flow the success message
// advertises round-trips with no manual git plumbing.
func TestRunCreate_StandaloneCreatesGitRepo(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	project := t.TempDir()
	t.Chdir(project)
	withCapturingPrinter(t, output.FormatText)

	t.Cleanup(func() { createStandalone = false; createType = "simple" })
	createStandalone = true
	createType = "simple"

	if err := runCreate(createCmd, []string{"demo"}); err != nil {
		t.Fatalf("runCreate: %v", err)
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

// TestRunCreate_ProjectScopedCreatesGitRepo is the same guard for the default
// project-scoped path: the edit dir must be a repo from birth, like a
// worktree-ejected edit dir.
func TestRunCreate_ProjectScopedCreatesGitRepo(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	project := t.TempDir()
	t.Chdir(project)
	withCapturingPrinter(t, output.FormatText)

	t.Cleanup(func() {
		createStandalone = false
		createType = "simple"
		createTarget = "claude"
		createGlobal = false
	})
	createStandalone = false
	createType = "simple"
	createTarget = "claude"

	if err := runCreate(createCmd, []string{"demo"}); err != nil {
		t.Fatalf("runCreate: %v", err)
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

// TestRunCreate_StandaloneInsideWorkTree_Refuses is the #241 gitlink-trap
// guard: a standalone scaffold inside an existing git work tree would be
// recorded by `git add` as a gitlink (pointer, no files), so create must
// refuse — for both a normal repo (.git directory) and a linked worktree
// (.git file).
func TestRunCreate_StandaloneInsideWorkTree_Refuses(t *testing.T) {
	cases := []struct {
		name    string
		gitKind string // "dir" or "file"
	}{
		{"normal repo (.git dir)", "dir"},
		{"linked worktree (.git file)", "file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("QUIVER_HOME", t.TempDir())
			project := t.TempDir()
			switch tc.gitKind {
			case "dir":
				if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
					t.Fatalf("mkdir .git: %v", err)
				}
			case "file":
				if err := os.WriteFile(filepath.Join(project, ".git"),
					[]byte("gitdir: /elsewhere/.git/worktrees/x\n"), 0o644); err != nil {
					t.Fatalf("write .git file: %v", err)
				}
			}
			// Run from a subdirectory to prove the walk-up detection.
			sub := filepath.Join(project, "skills")
			if err := os.MkdirAll(sub, 0o755); err != nil {
				t.Fatalf("mkdir sub: %v", err)
			}
			t.Chdir(sub)
			withCapturingPrinter(t, output.FormatText)

			t.Cleanup(func() { createStandalone = false; createType = "simple" })
			createStandalone = true
			createType = "simple"

			err := runCreate(createCmd, []string{"demo"})
			if err == nil {
				t.Fatal("standalone create inside a work tree returned nil; want the gitlink refusal (#241)")
			}
			if !strings.Contains(err.Error(), "gitlink") {
				t.Errorf("error = %v, want it to explain the gitlink trap", err)
			}
			if _, serr := os.Stat(filepath.Join(sub, "demo")); !os.IsNotExist(serr) {
				t.Errorf("refused create left a scaffold behind (stat err: %v)", serr)
			}
		})
	}
}
