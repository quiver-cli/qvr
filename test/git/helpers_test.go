package gittests

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	gogitcfg "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// testSkills is the shared fixture used by both gogit and worktree tests.
var testSkills = map[string]string{
	"code-review": `---
name: code-review
description: Performs code review on pull requests.
metadata:
  author: test-org
---

# Code Review
`,
	"deploy-helper": `---
name: deploy-helper
description: Helps with deployment tasks.
---

# Deploy Helper
`,
}

// setupTestRepo creates a non-bare repo with a skills/ directory containing
// the given skills, commits, and returns the repo path.
func setupTestRepo(t *testing.T, skills map[string]string) string {
	t.Helper()
	dir := t.TempDir()

	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init repo: %v", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}

	for name, content := range skills {
		skillDir := filepath.Join(dir, "skills", name)
		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := wt.Add(filepath.Join("skills", name, "SKILL.md")); err != nil {
			t.Fatalf("add: %v", err)
		}
	}

	_, err = wt.Commit("initial commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@test.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	return dir
}

// setupTestBareRepo creates a non-bare repo with skills, then bare-clones it.
// Returns the bare repo path.
func setupTestBareRepo(t *testing.T, skills map[string]string) string {
	t.Helper()
	srcDir := setupTestRepo(t, skills)
	bareDir := t.TempDir()

	_, err := gogit.PlainClone(bareDir, true, &gogit.CloneOptions{
		URL: srcDir,
	})
	if err != nil {
		t.Fatalf("bare clone: %v", err)
	}

	return bareDir
}

// setupBareRegistry creates a bare "upstream" repo, seeds it with skills from
// a transient working clone, and returns the bare's path. The returned path
// serves as the "real" remote URL for tests — bare repos accept pushes to
// their current branch, so push/pull round-trips work without extra setup.
func setupBareRegistry(t *testing.T, skills map[string]string) string {
	t.Helper()
	remoteBare := filepath.Join(t.TempDir(), "remote.git")
	if _, err := gogit.PlainInit(remoteBare, true); err != nil {
		t.Fatalf("init remote bare: %v", err)
	}

	seedDir := t.TempDir()
	seedRepo, err := gogit.PlainInit(seedDir, false)
	if err != nil {
		t.Fatalf("init seed: %v", err)
	}
	if _, err := seedRepo.CreateRemote(&gogitcfg.RemoteConfig{
		Name: "origin",
		URLs: []string{remoteBare},
	}); err != nil {
		t.Fatalf("create remote: %v", err)
	}

	wt, err := seedRepo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	for name, content := range skills {
		skillDir := filepath.Join(seedDir, "skills", name)
		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := wt.Add(filepath.Join("skills", name, "SKILL.md")); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	_, err = wt.Commit("initial", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "t@t", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	head, err := seedRepo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if err := seedRepo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"), head.Hash(),
	)); err != nil {
		t.Fatalf("set main: %v", err)
	}
	if err := seedRepo.Push(&gogit.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []gogitcfg.RefSpec{"refs/heads/main:refs/heads/main"},
	}); err != nil {
		t.Fatalf("push seed: %v", err)
	}

	remoteRepo, err := gogit.PlainOpen(remoteBare)
	if err != nil {
		t.Fatalf("open remote: %v", err)
	}
	if err := remoteRepo.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.HEAD, plumbing.NewBranchReferenceName("main"),
	)); err != nil {
		t.Fatalf("set remote HEAD: %v", err)
	}

	return remoteBare
}

// bareCloneFor mirrors what the registry manager does: bare-clone the remote
// and return the bare repo path that the installer will worktree from.
func bareCloneFor(t *testing.T, remoteURL string) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "registry.git")
	if _, err := gogit.PlainClone(bare, true, &gogit.CloneOptions{
		URL:    remoteURL,
		Mirror: true,
	}); err != nil {
		t.Fatalf("bare clone: %v", err)
	}
	return bare
}
