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
	"github.com/quiver-cli/qvr/internal/registry"
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

// Issue #156: `qvr lock verify` must exit non-zero on detected drift so CI
// can gate on supply-chain drift. Pre-fix it only failed under --frozen/
// --strict and exited 0 by default. Drives lockVerifyFailure across the
// --fail-on vocabulary and the legacy shorthands.
func TestLockVerifyFailure_ExitContract(t *testing.T) {
	drift := VerifySummary{OK: 4, Drift: 1}
	unver := VerifySummary{OK: 4, Unverified: 1}
	missing := VerifySummary{OK: 4, Missing: 1}
	clean := VerifySummary{OK: 5}

	cases := []struct {
		name             string
		failOn           string
		frozen, strict   bool
		summary          VerifySummary
		wantFail         bool
		wantErr          bool
		wantMsgSubstring string
	}{
		{"default fails on drift", "drift", false, false, drift, true, false, "drift=1"},
		{"default fails on missing", "drift", false, false, missing, true, false, "missing=1"},
		{"default ignores unverified", "drift", false, false, unver, false, false, ""},
		{"default clean exits 0", "drift", false, false, clean, false, false, ""},
		{"fail-on none ignores drift", "none", false, false, drift, false, false, ""},
		{"fail-on unverified catches unverified", "unverified", false, false, unver, true, false, "unverified=1"},
		{"frozen still fails on drift", "drift", true, false, drift, true, false, "drift=1"},
		{"frozen raises none floor", "none", true, false, drift, true, false, "drift=1"},
		{"strict catches unverified over none", "none", false, true, unver, true, false, "unverified=1"},
		{"invalid fail-on errors", "bogus", false, false, clean, false, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lockVerifyFailOn, lockVerifyFrozen, lockVerifyStrict = c.failOn, c.frozen, c.strict
			t.Cleanup(func() {
				lockVerifyFailOn, lockVerifyFrozen, lockVerifyStrict = "drift", false, false
			})
			msg, err := lockVerifyFailure(c.summary)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error for fail-on %q, got nil", c.failOn)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if (msg != "") != c.wantFail {
				t.Fatalf("failure=%q (fail=%v), want fail=%v", msg, msg != "", c.wantFail)
			}
			if c.wantMsgSubstring != "" && !strings.Contains(msg, c.wantMsgSubstring) {
				t.Errorf("failure %q missing %q", msg, c.wantMsgSubstring)
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

// TestLockUpgrade_BackfillsRootLayoutHash is the direct #151/#154 repro: a
// root-layout entry (path ".") with an empty SubtreeHash used to make
// `qvr lock upgrade` die with "canonical hash: locate subtree \".\":
// directory not found" and never fill the hash. It must now backfill cleanly.
func TestLockUpgrade_BackfillsRootLayoutHash(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	if err := config.Save(&config.Config{}); err != nil {
		t.Fatalf("save cfg: %v", err)
	}

	reg, name, commit := "rootreg", "solo", "abc1234"
	worktree := registry.WorktreePath(reg, name, registry.ShortSHA(commit))
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	// SKILL.md at the worktree ROOT — the path:"." layout.
	if err := os.WriteFile(filepath.Join(worktree, "SKILL.md"),
		[]byte("---\nname: solo\ndescription: root skill.\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	repo, err := gogit.PlainInit(worktree, false)
	if err != nil {
		t.Fatalf("git init: %v", err)
	}
	wt, _ := repo.Worktree()
	if _, err := wt.Add("SKILL.md"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := wt.Commit("init", &gogit.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t", When: time.Unix(0, 0).UTC()},
	}); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	project := t.TempDir()
	lockPath := filepath.Join(project, model.LockFileName)
	lock := model.NewLockFile(lockPath)
	lock.Put(&model.LockEntry{
		Name:     name,
		Registry: reg,
		Source:   "git@example.test:" + reg + ".git",
		Path:     ".", // root layout — the broken case
		Ref:      "main",
		Commit:   commit,
	})
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	withCapturingPrinter(t, "text")
	lockUpgradeDryRun = false
	t.Cleanup(func() { lockUpgradeDryRun = false })

	out, err := lockUpgradeInternal(lockPath)
	if err != nil {
		t.Fatalf("lockUpgradeInternal: %v", err)
	}
	for _, row := range out.Entries {
		if row.Name == name && row.Status == "skipped" {
			t.Fatalf("root-layout upgrade skipped (#151 dead-end): %s", row.Message)
		}
	}

	reread, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("reread lock: %v", err)
	}
	entry, err := reread.Get(name)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.SubtreeHash == "" {
		t.Error("upgrade did not backfill SubtreeHash for a path:'.' entry")
	}
}
