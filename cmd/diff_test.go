package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/raks097/quiver/internal/model"
)

func initWorktreeWithFile(t *testing.T, fileName, body string) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if _, err := wt.Add(fileName); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("init", &gogit.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t", When: time.Now()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return dir
}

func TestSkillDiff_DirtyWorktreeReturnsHunk(t *testing.T) {
	dir := initWorktreeWithFile(t, "SKILL.md", "hello\n")
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatalf("modify: %v", err)
	}

	entry := &model.LockEntry{Name: "demo", Worktree: dir}
	out, err := skillDiff(context.Background(), entry, false, false)
	if err != nil {
		t.Fatalf("skillDiff: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "SKILL.md") {
		t.Errorf("diff should mention the changed file:\n%s", got)
	}
	if !strings.Contains(got, "+world") {
		t.Errorf("diff should include the added line:\n%s", got)
	}
	if !strings.Contains(got, "@@") {
		t.Errorf("expected hunk header in unified diff:\n%s", got)
	}
}

func TestSkillDiff_CleanWorktreeIsEmpty(t *testing.T) {
	dir := initWorktreeWithFile(t, "SKILL.md", "hello\n")
	entry := &model.LockEntry{Name: "demo", Worktree: dir}
	out, err := skillDiff(context.Background(), entry, false, false)
	if err != nil {
		t.Fatalf("skillDiff: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty diff on clean tree, got %q", out)
	}
}

func TestSkillDiff_StagedFlag(t *testing.T) {
	dir := initWorktreeWithFile(t, "SKILL.md", "hello\n")
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatalf("modify: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if _, err := wt.Add("SKILL.md"); err != nil {
		t.Fatalf("stage: %v", err)
	}

	entry := &model.LockEntry{Name: "demo", Worktree: dir}

	unstaged, err := skillDiff(context.Background(), entry, false, false)
	if err != nil {
		t.Fatalf("unstaged diff: %v", err)
	}
	if len(unstaged) != 0 {
		t.Errorf("after staging, --no-cached diff should be empty, got %q", unstaged)
	}

	staged, err := skillDiff(context.Background(), entry, true, false)
	if err != nil {
		t.Fatalf("staged diff: %v", err)
	}
	if !strings.Contains(string(staged), "+world") {
		t.Errorf("--staged diff should include the staged change:\n%s", staged)
	}
}

func TestSkillDiff_StatFlag(t *testing.T) {
	dir := initWorktreeWithFile(t, "SKILL.md", "hello\n")
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatalf("modify: %v", err)
	}
	entry := &model.LockEntry{Name: "demo", Worktree: dir}
	out, err := skillDiff(context.Background(), entry, false, true)
	if err != nil {
		t.Fatalf("stat diff: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "SKILL.md") || !strings.Contains(got, "+") {
		t.Errorf("--stat output should summarize the change:\n%s", got)
	}
	if strings.Contains(got, "@@") {
		t.Errorf("--stat output should not contain hunk headers:\n%s", got)
	}
}
