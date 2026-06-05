package cmd

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	gogitcfg "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/quiver-cli/qvr/internal/config"
	"github.com/quiver-cli/qvr/internal/git"
	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/skill"
)

// installBranchPinned registers regName → a fresh remote carrying skill `name`
// on main and installs it pinned to the branch (entry.Ref="main"). Returns the
// remote path so a test can advance it. Reuses seedTaggedPullRemote from
// pull_test.go (the extra tag it creates is harmless here).
func installBranchPinned(t *testing.T, regName, name string) string {
	t.Helper()
	gc := git.NewGoGitClient()
	mgr := newRegistryManager(gc)
	remote := seedTaggedPullRemote(t, name, "v1.0.0")
	if _, err := mgr.Add(context.Background(), regName, remote); err != nil {
		t.Fatalf("registry add %s: %v", regName, err)
	}
	project, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	inst := skill.NewInstaller(mgr, git.NewGoGitWorktree(), gc)
	if _, err := inst.Install(skill.InstallRequest{
		Skill:       name + "@main",
		Targets:     []string{"claude"},
		ProjectRoot: project,
	}); err != nil {
		t.Fatalf("install %s@main: %v", name, err)
	}
	return remote
}

// advanceRemoteMain clones the remote, rewrites the skill's SKILL.md, commits,
// and pushes main — advancing the branch tip. Returns the new full commit hash.
func advanceRemoteMain(t *testing.T, remote, name, marker string) string {
	t.Helper()
	work := t.TempDir()
	r, err := gogit.PlainClone(work, false, &gogit.CloneOptions{
		URL:           remote,
		ReferenceName: plumbing.NewBranchReferenceName("main"),
	})
	if err != nil {
		t.Fatalf("clone for advance: %v", err)
	}
	wt, err := r.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	rel := "skills/" + name + "/SKILL.md"
	body := "---\nname: " + name + "\ndescription: advanced (" + marker + ")\n---\n# " + name + "\n" + marker + "\n"
	if err := os.WriteFile(work+"/"+rel, []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	if _, err := wt.Add(rel); err != nil {
		t.Fatalf("git add: %v", err)
	}
	h, err := wt.Commit("advance "+marker, &gogit.CommitOptions{
		Author: &object.Signature{Name: "T", Email: "t@t", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := r.Push(&gogit.PushOptions{RemoteName: "origin", RefSpecs: []gogitcfg.RefSpec{
		"refs/heads/main:refs/heads/main",
	}}); err != nil {
		t.Fatalf("push: %v", err)
	}
	return h.String()
}

func resetLockResolveFlags(t *testing.T) {
	t.Helper()
	// cmd.Context() is nil when RunE is invoked directly (cobra sets it during
	// Execute); give it a real context so the fetch path doesn't panic.
	lockCmd.SetContext(context.Background())
	t.Cleanup(func() {
		lockResolvePackage = ""
		lockResolveDryRun = false
		lockResolveGlobal = false
		lockCmd.SetContext(context.Background())
	})
	lockResolvePackage = ""
	lockResolveDryRun = false
	lockResolveGlobal = false
}

func lockEntryCommit(t *testing.T, lockPath, name string) string {
	t.Helper()
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	e, err := lock.Get(name)
	if err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	return e.Commit
}

// TestRunLock_RepinsAdvancedBranch is the core re-resolve: after the upstream
// branch advances, `qvr lock` re-pins the recorded commit to the new tip and
// invalidates the content hash (the next sync refills it).
func TestRunLock_RepinsAdvancedBranch(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Chdir(t.TempDir())
	resetLockResolveFlags(t)
	resetPrinter(t)

	remote := installBranchPinned(t, "acme", "demo")
	project, _ := os.Getwd()
	lockPath := model.DefaultLockPath(project, config.Dir(), false)
	c1 := lockEntryCommit(t, lockPath, "demo")

	newHash := advanceRemoteMain(t, remote, "demo", "v2")
	if newHash == c1 {
		t.Fatal("advance produced the same commit")
	}

	if err := runLock(lockCmd, nil); err != nil {
		t.Fatalf("runLock: %v", err)
	}

	got := lockEntryCommit(t, lockPath, "demo")
	if got != newHash {
		t.Errorf("commit = %s, want re-pinned %s", got, newHash)
	}
	// The hash is recomputed from the bare clone's objects (no checkout), so
	// the re-pinned entry is immediately verifiable — not left empty.
	lock, _ := model.ReadLockFile(lockPath)
	e, _ := lock.Get("demo")
	if !strings.HasPrefix(e.SubtreeHash, "sha256:") {
		t.Errorf("re-pin should recompute subtreeHash from objects, got %q", e.SubtreeHash)
	}
}

// TestRunLock_ThenSyncIsGreen is the end-to-end contract: a re-pin recomputes
// the content hash from objects, so the immediately following `qvr sync`
// materialises the new commit and `qvr sync --check` reports in-sync — no
// integrity failure, no manual `qvr lock upgrade` step in between.
func TestRunLock_ThenSyncIsGreen(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Chdir(t.TempDir())
	resetLockResolveFlags(t)
	resetSyncModeFlags(t)
	resetPrinter(t)

	remote := installBranchPinned(t, "acme", "demo")
	advanceRemoteMain(t, remote, "demo", "v2")

	if err := runLock(lockCmd, nil); err != nil {
		t.Fatalf("runLock: %v", err)
	}
	if err := runSync(syncCmd, nil); err != nil {
		t.Fatalf("sync after re-pin should be green: %v", err)
	}
	syncCheck = true
	if err := runSync(syncCmd, nil); err != nil {
		t.Fatalf("sync --check after re-pin+sync should be green: %v", err)
	}
}

// TestRunLock_DryRunDoesNotWrite confirms --dry-run previews the re-pin but
// leaves qvr.lock byte-identical.
func TestRunLock_DryRunDoesNotWrite(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Chdir(t.TempDir())
	resetLockResolveFlags(t)
	resetPrinter(t)

	remote := installBranchPinned(t, "acme", "demo")
	project, _ := os.Getwd()
	lockPath := model.DefaultLockPath(project, config.Dir(), false)
	advanceRemoteMain(t, remote, "demo", "v2")

	before, _ := os.ReadFile(lockPath)
	lockResolveDryRun = true
	if err := runLock(lockCmd, nil); err != nil {
		t.Fatalf("runLock --dry-run: %v", err)
	}
	after, _ := os.ReadFile(lockPath)
	if string(before) != string(after) {
		t.Errorf("--dry-run mutated qvr.lock:\nbefore=%s\nafter=%s", before, after)
	}
}

// TestRunLock_PackageSelectsSingleSkill confirms -P re-pins only the named
// skill, leaving siblings untouched even though both advanced upstream.
func TestRunLock_PackageSelectsSingleSkill(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Chdir(t.TempDir())
	resetLockResolveFlags(t)
	resetPrinter(t)

	remoteA := installBranchPinned(t, "acme", "demo")
	remoteB := installBranchPinned(t, "beta", "other")
	project, _ := os.Getwd()
	lockPath := model.DefaultLockPath(project, config.Dir(), false)
	demo0 := lockEntryCommit(t, lockPath, "demo")
	other0 := lockEntryCommit(t, lockPath, "other")

	demoNew := advanceRemoteMain(t, remoteA, "demo", "v2")
	advanceRemoteMain(t, remoteB, "other", "v2")

	lockResolvePackage = "demo"
	if err := runLock(lockCmd, nil); err != nil {
		t.Fatalf("runLock -P demo: %v", err)
	}

	if got := lockEntryCommit(t, lockPath, "demo"); got != demoNew {
		t.Errorf("demo commit = %s, want re-pinned %s", got, demoNew)
	}
	if got := lockEntryCommit(t, lockPath, "other"); got != other0 {
		t.Errorf("other commit = %s, want untouched %s", got, other0)
	}
	if demo0 == demoNew {
		t.Fatal("demo did not actually advance")
	}
}
