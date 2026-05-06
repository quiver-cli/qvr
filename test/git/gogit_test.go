package gittests

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/raks097/quiver/internal/git"
)

func TestBareClone(t *testing.T) {
	srcDir := setupTestRepo(t, testSkills)
	destDir := filepath.Join(t.TempDir(), "bare.git")

	client := git.NewGoGitClient()
	err := client.BareClone(context.Background(), srcDir, destDir)
	if err != nil {
		t.Fatalf("BareClone: %v", err)
	}

	// Verify it's a bare repo (no worktree)
	_, err = os.Stat(filepath.Join(destDir, "HEAD"))
	if err != nil {
		t.Errorf("expected HEAD file in bare repo: %v", err)
	}
	_, err = os.Stat(filepath.Join(destDir, ".git"))
	if !os.IsNotExist(err) {
		t.Error("bare repo should not have .git subdirectory")
	}
}

func TestBareClone_AlreadyExists(t *testing.T) {
	srcDir := setupTestRepo(t, testSkills)
	destDir := filepath.Join(t.TempDir(), "bare.git")

	client := git.NewGoGitClient()
	if err := client.BareClone(context.Background(), srcDir, destDir); err != nil {
		t.Fatalf("first BareClone: %v", err)
	}
	err := client.BareClone(context.Background(), srcDir, destDir)
	if !errors.Is(err, git.ErrAlreadyExists) {
		t.Errorf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestClone(t *testing.T) {
	srcDir := setupTestRepo(t, testSkills)
	destDir := filepath.Join(t.TempDir(), "clone")

	client := git.NewGoGitClient()
	err := client.Clone(context.Background(), srcDir, destDir)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}

	// Verify it has a .git directory (non-bare)
	_, err = os.Stat(filepath.Join(destDir, ".git"))
	if err != nil {
		t.Errorf("expected .git directory: %v", err)
	}
}

func TestFetch(t *testing.T) {
	srcDir := setupTestRepo(t, testSkills)
	bareDir := filepath.Join(t.TempDir(), "bare.git")

	client := git.NewGoGitClient()
	if err := client.BareClone(context.Background(), srcDir, bareDir); err != nil {
		t.Fatalf("BareClone: %v", err)
	}

	// Add a new commit to the source repo
	srcRepo, err := gogit.PlainOpen(srcDir)
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	wt, err := srcRepo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "new-file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := wt.Add("new-file.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("add new file", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "t@t.com", When: time.Now()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if err := client.Fetch(context.Background(), bareDir); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
}

func TestFetch_AlreadyUpToDate(t *testing.T) {
	bareDir := setupTestBareRepo(t, testSkills)

	client := git.NewGoGitClient()
	err := client.Fetch(context.Background(), bareDir)
	if err != nil {
		t.Fatalf("Fetch (already up to date) should not error: %v", err)
	}
}

func TestListBranches(t *testing.T) {
	bareDir := setupTestBareRepo(t, testSkills)

	// Add extra branches directly on the bare repo
	bareRepo, err := gogit.PlainOpen(bareDir)
	if err != nil {
		t.Fatalf("open bare: %v", err)
	}
	head, err := bareRepo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if err := bareRepo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("develop"),
		head.Hash(),
	)); err != nil {
		t.Fatalf("set develop: %v", err)
	}
	if err := bareRepo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("feature-x"),
		head.Hash(),
	)); err != nil {
		t.Fatalf("set feature-x: %v", err)
	}

	client := git.NewGoGitClient()
	branches, err := client.ListBranches(bareDir)
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}

	names := make(map[string]bool)
	for _, b := range branches {
		names[b.Name] = true
	}

	for _, expected := range []string{"master", "develop", "feature-x"} {
		if !names[expected] {
			t.Errorf("expected branch %q not found in %v", expected, names)
		}
	}
}

func TestListTags(t *testing.T) {
	srcDir := setupTestRepo(t, testSkills)
	srcRepo, err := gogit.PlainOpen(srcDir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	head, err := srcRepo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}

	// Create lightweight tags
	if _, err := srcRepo.CreateTag("v1.0.0", head.Hash(), nil); err != nil {
		t.Fatalf("create tag v1.0.0: %v", err)
	}
	if _, err := srcRepo.CreateTag("v1.1.0", head.Hash(), nil); err != nil {
		t.Fatalf("create tag v1.1.0: %v", err)
	}

	bareDir := filepath.Join(t.TempDir(), "bare.git")
	if _, err := gogit.PlainClone(bareDir, true, &gogit.CloneOptions{URL: srcDir}); err != nil {
		t.Fatalf("bare clone: %v", err)
	}

	client := git.NewGoGitClient()
	tags, err := client.ListTags(bareDir)
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}

	if len(tags) != 2 {
		t.Fatalf("expected 2 tags, got %d", len(tags))
	}

	names := make(map[string]bool)
	for _, tag := range tags {
		names[tag.Name] = true
		if !tag.IsTag {
			t.Errorf("expected IsTag=true for %q", tag.Name)
		}
	}
	if !names["v1.0.0"] || !names["v1.1.0"] {
		t.Errorf("expected tags v1.0.0 and v1.1.0, got %v", names)
	}
}

func TestHeadCommit(t *testing.T) {
	bareDir := setupTestBareRepo(t, testSkills)

	client := git.NewGoGitClient()
	hash, err := client.HeadCommit(bareDir)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	if len(hash) != 40 {
		t.Errorf("expected 40-char hash, got %q", hash)
	}
}

func TestDefaultBranch(t *testing.T) {
	bareDir := setupTestBareRepo(t, testSkills)

	client := git.NewGoGitClient()
	branch, err := client.DefaultBranch(bareDir)
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	if branch != "master" {
		t.Errorf("expected master, got %q", branch)
	}
}

func TestReadBlob(t *testing.T) {
	bareDir := setupTestBareRepo(t, testSkills)

	client := git.NewGoGitClient()
	data, err := client.ReadBlob(bareDir, "HEAD", "skills/code-review/SKILL.md")
	if err != nil {
		t.Fatalf("ReadBlob: %v", err)
	}

	content := string(data)
	if len(content) == 0 {
		t.Error("expected non-empty content")
	}
	if !strings.Contains(content, "code-review") {
		t.Errorf("expected content to contain 'code-review', got %q", content)
	}
}

func TestReadBlob_NotFound(t *testing.T) {
	bareDir := setupTestBareRepo(t, testSkills)

	client := git.NewGoGitClient()
	_, err := client.ReadBlob(bareDir, "HEAD", "nonexistent/file.md")
	if !errors.Is(err, git.ErrBlobNotFound) {
		t.Errorf("expected ErrBlobNotFound, got %v", err)
	}
}

func TestListTree(t *testing.T) {
	bareDir := setupTestBareRepo(t, testSkills)

	client := git.NewGoGitClient()
	entries, err := client.ListTree(bareDir, "HEAD", "skills")
	if err != nil {
		t.Fatalf("ListTree: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries under skills/, got %d", len(entries))
	}

	names := make(map[string]bool)
	for _, e := range entries {
		names[e.Name] = true
		if !e.IsDir {
			t.Errorf("expected directory, got file: %s", e.Name)
		}
	}
	if !names["code-review"] || !names["deploy-helper"] {
		t.Errorf("expected code-review and deploy-helper, got %v", names)
	}
}

func TestListTree_NotFound(t *testing.T) {
	bareDir := setupTestBareRepo(t, testSkills)

	client := git.NewGoGitClient()
	_, err := client.ListTree(bareDir, "HEAD", "nonexistent")
	if !errors.Is(err, git.ErrTreeNotFound) {
		t.Errorf("expected ErrTreeNotFound, got %v", err)
	}
}

func TestLsRemote(t *testing.T) {
	srcDir := setupTestRepo(t, testSkills)

	client := git.NewGoGitClient()
	refs, err := client.LsRemote(context.Background(), srcDir)
	if err != nil {
		t.Fatalf("LsRemote: %v", err)
	}

	if len(refs.Refs) == 0 {
		t.Error("expected at least one ref")
	}

	// Should have HEAD and refs/heads/master
	if _, ok := refs.Refs["HEAD"]; !ok {
		t.Error("expected HEAD ref")
	}
}

func TestResolveRef_Tag(t *testing.T) {
	srcDir := setupTestRepo(t, testSkills)
	srcRepo, err := gogit.PlainOpen(srcDir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	head, err := srcRepo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if _, err := srcRepo.CreateTag("v2.0.0", head.Hash(), nil); err != nil {
		t.Fatalf("create tag: %v", err)
	}

	bareDir := filepath.Join(t.TempDir(), "bare.git")
	if _, err := gogit.PlainClone(bareDir, true, &gogit.CloneOptions{URL: srcDir}); err != nil {
		t.Fatalf("bare clone: %v", err)
	}

	client := git.NewGoGitClient()
	data, err := client.ReadBlob(bareDir, "v2.0.0", "skills/code-review/SKILL.md")
	if err != nil {
		t.Fatalf("ReadBlob via tag: %v", err)
	}
	if !strings.Contains(string(data), "code-review") {
		t.Error("expected content via tag ref")
	}
}
