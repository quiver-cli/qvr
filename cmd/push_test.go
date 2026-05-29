package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/registry"
)

// seedPushTestEntry creates a worktree at the EntryWorktreePath the dry-run
// path will derive, committed once so the working tree starts clean. Returns
// the worktree dir + the lock-tracked entry. Caller sets dirty content on
// top to exercise the dry-run preview.
func seedPushTestEntry(t *testing.T) (entry *model.LockEntry, worktree string) {
	t.Helper()
	t.Setenv("QUIVER_HOME", t.TempDir())
	reg, name, commit := "ptest", "demo", "abc1234"
	worktree = registry.WorktreePath(reg, name, registry.ShortSHA(commit))
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	repo, err := gogit.PlainInit(worktree, false)
	if err != nil {
		t.Fatalf("git init: %v", err)
	}
	skillRel := filepath.Join("skills", "demo")
	if err := os.MkdirAll(filepath.Join(worktree, skillRel), 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, skillRel, "SKILL.md"),
		[]byte("---\nname: demo\n---\noriginal\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	wt, _ := repo.Worktree()
	if _, err := wt.Add(filepath.Join(skillRel, "SKILL.md")); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := wt.Commit("init", &gogit.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t", When: time.Unix(0, 0).UTC()},
	}); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	entry = &model.LockEntry{
		Name:     name,
		Registry: reg,
		Source:   "git@example.test:" + reg + ".git",
		Path:     skillRel,
		Ref:      "main",
		Commit:   commit,
	}
	return entry, worktree
}

// withCapturingPrinter swaps the package-global printer for a buffer-backed
// one for the duration of the test and returns the stdout buffer. Reused
// for the dry-run output assertions.
func withCapturingPrinter(t *testing.T, format output.Format) *bytes.Buffer {
	t.Helper()
	stdout := &bytes.Buffer{}
	prev := printer
	printer = &output.Printer{Out: stdout, Err: &bytes.Buffer{}, Format: format}
	t.Cleanup(func() { printer = prev })
	return stdout
}

// Issue #67: --dry-run prints a planned commit (target branch, files, message,
// author) and does not push, commit, or touch the lock.
func TestPushDryRun_EmitsPlanWithoutMutating(t *testing.T) {
	entry, worktree := seedPushTestEntry(t)

	// Project lock referencing the entry.
	project := t.TempDir()
	lock := model.NewLockFile(filepath.Join(project, model.LockFileName))
	lock.Put(entry)
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	// Make the worktree dirty so the dry-run has files to list.
	skillFile := filepath.Join(worktree, "skills/demo/SKILL.md")
	if err := os.WriteFile(skillFile, []byte("---\nname: demo\n---\nedited\n"), 0o644); err != nil {
		t.Fatalf("dirty: %v", err)
	}

	stdout := withCapturingPrinter(t, output.FormatJSON)
	pushMessage = "test commit"
	pushAuthor = "alice"
	pushEmail = "a@example"
	t.Cleanup(func() {
		pushMessage = ""
		pushAuthor = ""
		pushEmail = ""
	})

	if err := runPushDryRun(entry.Name, filepath.Join(project, model.LockFileName)); err != nil {
		t.Fatalf("runPushDryRun: %v", err)
	}

	var got struct {
		DryRun  bool     `json:"dry_run"`
		Planned pushPlan `json:"planned"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal dry-run payload: %v\nbody: %s", err, stdout.String())
	}
	if !got.DryRun {
		t.Errorf("dry_run flag missing in payload: %+v", got)
	}
	if got.Planned.Branch != "main" {
		t.Errorf("planned branch = %q, want main", got.Planned.Branch)
	}
	if got.Planned.Message != "test commit" {
		t.Errorf("planned message lost: %q", got.Planned.Message)
	}
	if got.Planned.Author != "alice" || got.Planned.Email != "a@example" {
		t.Errorf("planned author lost: %+v", got.Planned)
	}
	if got.Planned.NoChanges {
		t.Errorf("dirty worktree should not be reported as no_changes")
	}
	if len(got.Planned.Files) == 0 {
		t.Errorf("expected at least one dirty file in plan, got %+v", got.Planned.Files)
	}

	// Regression: confirm the lock file was NOT rewritten — entry.Commit
	// must still be the install-time SHA.
	reread, err := model.ReadLockFile(filepath.Join(project, model.LockFileName))
	if err != nil {
		t.Fatalf("reread lock: %v", err)
	}
	after, _ := reread.Get(entry.Name)
	if after.Commit != entry.Commit {
		t.Errorf("dry-run mutated lock entry.Commit: was %q, now %q", entry.Commit, after.Commit)
	}

	// Worktree HEAD must still point at the original commit — no
	// commit/push happened.
	repo, err := gogit.PlainOpen(worktree)
	if err != nil {
		t.Fatalf("open worktree: %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	cIter, err := repo.Log(&gogit.LogOptions{From: head.Hash()})
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	count := 0
	_ = cIter.ForEach(func(*object.Commit) error { count++; return nil })
	if count != 1 {
		t.Errorf("dry-run created new commits — log has %d entries, want 1", count)
	}
}

// Clean worktree → NoChanges=true, no Files listed, exit 0.
func TestPushDryRun_CleanWorktreeReportsNoChanges(t *testing.T) {
	entry, _ := seedPushTestEntry(t)

	project := t.TempDir()
	lock := model.NewLockFile(filepath.Join(project, model.LockFileName))
	lock.Put(entry)
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	stdout := withCapturingPrinter(t, output.FormatJSON)
	if err := runPushDryRun(entry.Name, filepath.Join(project, model.LockFileName)); err != nil {
		t.Fatalf("runPushDryRun: %v", err)
	}

	var got struct {
		Planned pushPlan `json:"planned"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, stdout.String())
	}
	if !got.Planned.NoChanges {
		t.Errorf("clean worktree should have no_changes=true, got %+v", got.Planned)
	}
	if len(got.Planned.Files) != 0 {
		t.Errorf("clean worktree should have empty files list, got %+v", got.Planned.Files)
	}
}

func TestPushDryRun_RejectsLinkInstall(t *testing.T) {
	project := t.TempDir()
	lock := model.NewLockFile(filepath.Join(project, model.LockFileName))
	lock.Put(&model.LockEntry{
		Name:   "linked",
		Source: "/some/local/path",
		Ref:    "local",
	})
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	withCapturingPrinter(t, output.FormatJSON)
	if err := runPushDryRun("linked", filepath.Join(project, model.LockFileName)); err == nil {
		t.Error("expected error rejecting link-install dry-run, got nil")
	}
}
