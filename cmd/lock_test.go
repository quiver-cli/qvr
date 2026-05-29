package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/registry"
)

// Regression for #20 + #21: the message names actual failing categories
// rather than the legacy hardcoded "drift detected".
func TestFailureCategories_onlyNonZeroListed(t *testing.T) {
	cases := []struct {
		name string
		in   VerifySummary
		want string
	}{
		{"missing only", VerifySummary{Missing: 1}, "missing=1"},
		{"drift + missing", VerifySummary{Drift: 2, Missing: 1}, "drift=2, missing=1"},
		{"failed only", VerifySummary{Failed: 3}, "failed=3"},
		{"drift + failed", VerifySummary{Drift: 1, Failed: 2}, "drift=1, failed=2"},
		{"empty", VerifySummary{}, "no failing entries"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := failureCategories(c.in)
			if got != c.want {
				t.Errorf("failureCategories(%+v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// The v2→v3 provenance backfill tests were removed when v5 hard-broke the
// migration path. v4-and-older locks now error out at ReadLockFile with
// "delete qvr.lock and run qvr sync" — see internal/model/lockfile_test.go's
// TestLockFile_RejectsUnsupportedVersion. `qvr lock upgrade` in v5
// backfills missing SubtreeHash AND, when the scan gate is configured,
// re-runs the scan to repopulate verification.scan (issue #63).

// Issue #63 regression: `qvr lock upgrade` must repopulate the
// verification.scan block on entries missing one when the scan gate is
// configured. Pre-fix the upgrade only refilled top-level SubtreeHash,
// and the help text's "populate[s] Verification blocks" promise lied.
func TestLockUpgrade_RepopulatesVerificationScanAndSubtreeHash(t *testing.T) {
	// Single QUIVER_HOME houses both the worktrees and config — config
	// reads from $QUIVER_HOME/config.yaml so a second t.Setenv would
	// silently shadow this one and leave scan_on_install=false in the
	// loaded config (the bug the earlier test draft hit).
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	cfg := &config.Config{Security: config.SecurityConfig{ScanOnInstall: true}}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save cfg: %v", err)
	}

	reg, name, commit := "upr", "demo", "abc1234"
	worktree := registry.WorktreePath(reg, name, registry.ShortSHA(commit))
	skillRel := filepath.Join("skills", "demo")
	if err := os.MkdirAll(filepath.Join(worktree, skillRel), 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, skillRel, "SKILL.md"),
		[]byte("---\nname: demo\ndescription: test\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	// HashSubtree reads via go-git, so the worktree must be a real git
	// repo with at least one commit. Without this the hash computation
	// fails and the test's setup doesn't actually exercise the upgrade
	// path.
	repo, err := gogit.PlainInit(worktree, false)
	if err != nil {
		t.Fatalf("git init: %v", err)
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

	// Build a lock with neither SubtreeHash nor Verification on the
	// entry — the post-fix "user hand-edited to strip both" repro from
	// the issue.
	project := t.TempDir()
	lockPath := filepath.Join(project, model.LockFileName)
	lock := model.NewLockFile(lockPath)
	lock.Put(&model.LockEntry{
		Name:     name,
		Registry: reg,
		Source:   "git@example.test:" + reg + ".git",
		Path:     skillRel,
		Ref:      "main",
		Commit:   commit,
	})
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	withCapturingPrinter(t, "text")
	lockUpgradeDryRun = false
	t.Cleanup(func() { lockUpgradeDryRun = false })

	if _, err := lockUpgradeInternal(lockPath); err != nil {
		t.Fatalf("lockUpgradeInternal: %v", err)
	}

	// Read back and assert both fields are filled.
	reread, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("reread lock: %v", err)
	}
	entry, err := reread.Get(name)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.SubtreeHash == "" {
		t.Errorf("upgrade did not fill SubtreeHash")
	}
	if entry.Verification == nil || entry.Verification.Scan == nil {
		t.Errorf("upgrade did not repopulate verification.scan: %+v", entry.Verification)
	}
	// Sanity: ScannerVersion stamp is present so `qvr lock verify`
	// downstream can detect drift across binary upgrades.
	if entry.Verification != nil && entry.Verification.Scan != nil && entry.Verification.Scan.ScannerVersion == "" {
		t.Errorf("scan ref missing ScannerVersion")
	}
}
