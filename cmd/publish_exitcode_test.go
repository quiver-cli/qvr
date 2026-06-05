package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/quiver-cli/qvr/internal/config"
	"github.com/quiver-cli/qvr/internal/model"
)

// publishExitCodeFixture stands up a minimal edit-mode project: a project
// root with a .claude/skills/<name>/ that is itself a real git repo (so
// ResolveEntryHeadCommit can read HEAD) containing a SKILL.md and one
// committed file. Returns the project root, the absolute edit dir, and
// the eject-dir HEAD SHA so callers can pin entry.Commit precisely.
func publishExitCodeFixture(t *testing.T, name string) (project, editAbs, headSHA string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	if err := config.Save(&config.Config{}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	project = t.TempDir()

	editRel := filepath.Join(".claude", "skills", name)
	editAbs = filepath.Join(project, editRel)
	if err := os.MkdirAll(editAbs, 0o755); err != nil {
		t.Fatalf("mkdir edit dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(editAbs, "SKILL.md"),
		[]byte("---\nname: "+name+"\ndescription: exit-code fixture\n---\n# "+name+"\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	repo, err := gogit.PlainInit(editAbs, false)
	if err != nil {
		t.Fatalf("git init edit dir: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if _, err := wt.Add("SKILL.md"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	hash, err := wt.Commit("seed", &gogit.CommitOptions{
		Author: &object.Signature{Name: "T", Email: "t@t", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("git commit: %v", err)
	}

	lockPath := filepath.Join(project, model.LockFileName)
	lock := model.NewLockFile(lockPath)
	lock.Put(&model.LockEntry{
		Name:        name,
		Mode:        model.ModeEdit,
		EditPath:    editRel,
		Source:      filepath.Join(t.TempDir(), "remote.git"), // bogus remote — we only test refusal paths that fire before push
		Ref:         "main",
		Commit:      hash.String(),
		Targets:     []string{"claude"},
		InstalledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	return project, editAbs, hash.String()
}

// resetPublishFlags pins package-level publish flags to their cobra
// defaults so a prior test's --auto-commit / --allow-lockfile-heal /
// --no-scan setting doesn't bleed in.
func resetPublishFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		publishRegistry = ""
		publishBranch = ""
		publishTag = ""
		publishMessage = ""
		publishAuthor = ""
		publishEmail = ""
		publishDryRun = false
		publishNoCreateBranch = false
		publishNoScan = false
		publishGlobal = false
		publishFork = ""
		publishMigrate = false
		publishAllowHeal = false
		publishAutoCommit = false
		publishForce = false
		publishLayout = ""
	})
	publishRegistry = ""
	publishBranch = ""
	publishTag = ""
	publishMessage = ""
	publishAuthor = ""
	publishEmail = ""
	publishDryRun = false
	publishNoCreateBranch = false
	publishNoScan = true // skip the scan path so refusal tests don't depend on the scanner finding anything
	publishGlobal = false
	publishFork = ""
	publishMigrate = false
	publishAllowHeal = false
	publishAutoCommit = false
	publishForce = false
	publishLayout = ""
}

// TestRunPublish_LockfileTamper_ReturnsErrorWithIssueTag pins the
// lockfile-tamper refusal at the cobra boundary (issue #74). When
// qvr.lock's commit doesn't match the edit repo's HEAD, runPublish must
// return a non-nil error — relying on cobra.SilenceErrors + Execute() to
// translate that into a non-zero exit. The error must cite "(issue #74)"
// so CI scripts can grep one stable string regardless of which guard
// fired.
func TestRunPublish_LockfileTamper_ReturnsErrorWithIssueTag(t *testing.T) {
	project, _, headSHA := publishExitCodeFixture(t, "demo")
	t.Chdir(project)
	resetPublishFlags(t)
	withCapturingPrinter(t, "text")

	// Tamper: rewrite entry.Commit to a SHA that isn't reachable from HEAD.
	lockPath := filepath.Join(project, model.LockFileName)
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	e, err := lock.Get("demo")
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if e.Commit != headSHA {
		t.Fatalf("fixture out of sync: entry.Commit %s vs head %s", e.Commit, headSHA)
	}
	e.Commit = "deadbeef00000000000000000000000000000000"
	lock.Put(e)
	if err := lock.Write(); err != nil {
		t.Fatalf("write tampered lock: %v", err)
	}

	err = runPublish(publishCmd, []string{"demo"})
	if err == nil {
		t.Fatal("runPublish returned nil on tampered lockfile commit — issue #74 regression: cobra would exit 0")
	}
	if !strings.Contains(err.Error(), "issue #74") {
		t.Errorf("error %q missing \"issue #74\" tag — CI grep target broken", err.Error())
	}
}

// TestRunPublish_DirtyEditDir_ReturnsErrorWithIssueTag pins the
// dirty-WD refusal (issue #83 trigger, issue #74 exit-code shape).
// Without --auto-commit, publish must refuse uncommitted edits and the
// error must propagate to a non-zero exit. Tag is "(issue #74)" so the
// refusal-set grep is uniform across guards.
func TestRunPublish_DirtyEditDir_ReturnsErrorWithIssueTag(t *testing.T) {
	project, editAbs, _ := publishExitCodeFixture(t, "demo")
	t.Chdir(project)
	resetPublishFlags(t)
	withCapturingPrinter(t, "text")

	// Dirty the edit dir with an uncommitted file. AutoCommit defaults
	// to false (resetPublishFlags pins this), so publish should refuse.
	if err := os.WriteFile(filepath.Join(editAbs, "WIP.md"),
		[]byte("# uncommitted WIP\n"), 0o644); err != nil {
		t.Fatalf("write WIP: %v", err)
	}

	err := runPublish(publishCmd, []string{"demo"})
	if err == nil {
		t.Fatal("runPublish returned nil on dirty edit dir — issue #74 regression: cobra would exit 0")
	}
	if !strings.Contains(err.Error(), "issue #74") {
		t.Errorf("error %q missing \"issue #74\" tag — CI grep target broken", err.Error())
	}
	if !strings.Contains(err.Error(), "auto-commit") {
		t.Errorf("error %q should mention --auto-commit so the user knows the escape hatch", err.Error())
	}
}
