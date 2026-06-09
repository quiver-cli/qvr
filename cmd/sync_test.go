package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astra-sh/qvr/internal/canonical"
	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/astra-sh/qvr/internal/skill"
)

// captureSyncStderr swaps the package printer for one with a stderr buffer
// so a test can assert on warning/error lines (which Printer.Warning and
// Printer.Error route to Err). Restores the previous printer on cleanup.
// Used here instead of withCapturingPrinter because that helper discards
// the stderr buffer and these tests need to assert on warnings.
func captureSyncStderr(t *testing.T, fn func()) string {
	t.Helper()
	stderr := &bytes.Buffer{}
	prev := printer
	printer = &output.Printer{Out: &bytes.Buffer{}, Err: stderr, Format: output.FormatText}
	t.Cleanup(func() { printer = prev })
	fn()
	return stderr.String()
}

// TestRenderDriftReport_CommitDriftDistinguishedFromSubtreeHash is the
// surfacing guard for issue #73: when VerifySingleEntry packs a commit-
// only tamper into the Drift slice, sync must render a dedicated "commit
// drift" line that cites issue #73 — not the generic "subtreeHash drift
// — recorded , on disk …" line that the pre-fix render emitted (which
// hid the commit-only drift from the user even though the verifier had
// flagged it).
func TestRenderDriftReport_CommitDriftDistinguishedFromSubtreeHash(t *testing.T) {
	cases := []struct {
		name      string
		report    skill.VerifyEntryResult
		wantInErr []string
	}{
		{
			name: "commit-only drift cites issue #73 and shows both hashes",
			report: skill.VerifyEntryResult{
				Name:   "ghost",
				Status: skill.VerifyStatusDrift,
				Drift: []skill.VerifyDriftItem{
					{Field: "commit", Expected: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", Actual: "0123456789abcdef0123456789abcdef01234567"},
				},
			},
			wantInErr: []string{"ghost", "commit drift", "issue #73", "deadbee", "0123456"},
		},
		{
			name: "subtreeHash drift keeps the actionable repair tip",
			report: skill.VerifyEntryResult{
				Name:   "demo",
				Status: skill.VerifyStatusDrift,
				Drift: []skill.VerifyDriftItem{
					{Field: "subtreeHash", Expected: "sha256:expected", Actual: "sha256:actual"},
				},
			},
			wantInErr: []string{"demo", "subtreeHash drift", "qvr lock verify --repair"},
		},
		{
			name: "mixed drift surfaces both lines so neither is hidden",
			report: skill.VerifyEntryResult{
				Name:   "both",
				Status: skill.VerifyStatusDrift,
				Drift: []skill.VerifyDriftItem{
					{Field: "subtreeHash", Expected: "sha256:expected", Actual: "sha256:actual"},
					{Field: "commit", Expected: "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111", Actual: "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222"},
				},
			},
			wantInErr: []string{"subtreeHash drift", "commit drift", "issue #73"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := captureSyncStderr(t, func() { renderDriftReport(tc.report) })
			for _, want := range tc.wantInErr {
				if !strings.Contains(err, want) {
					t.Errorf("renderDriftReport stderr missing %q\nfull output:\n%s", want, err)
				}
			}
		})
	}
}

// Regression for the CodeRabbit-flagged bug on autoRegisterRegistriesFromLock:
// `qvr sync --dry-run` used to call this helper unconditionally, which
// invoked config.Save and mutated ~/.quiver/config.yaml even though
// --dry-run's contract is "no filesystem changes." The fix threads
// dryRun through; in dry-run we still announce what would be registered
// on stderr but never persist.
func TestAutoRegisterRegistriesFromLock_DryRunDoesNotWriteConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	// Empty config baseline.
	if err := config.Save(&config.Config{}); err != nil {
		t.Fatalf("seed empty config: %v", err)
	}
	cfgPath := config.Path()
	originalBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read seed config: %v", err)
	}

	withCapturingPrinter(t, "text")
	lock := model.NewLockFile(filepath.Join(t.TempDir(), model.LockFileName))
	lock.Put(&model.LockEntry{
		Name:     "demo",
		Registry: "raks",
		Source:   "git@github.com:raks097/skills.git",
		Ref:      "main",
		Commit:   "abc1234",
	})

	autoRegisterRegistriesFromLock(lock, true /*dryRun*/)

	// Confirm the config file is byte-identical — no save call fired.
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("reread config: %v", err)
	}
	if string(after) != string(originalBytes) {
		t.Errorf("dry-run mutated config.yaml\nbefore: %s\nafter:  %s", originalBytes, after)
	}
	// And the in-memory loaded config still has no raks entry.
	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := loaded.Registries["raks"]; ok {
		t.Errorf("dry-run leaked raks into the persisted config: %+v", loaded.Registries)
	}
}

// TestRunSync_NoOpRerun_StdoutIsSingleLine is the user-facing guard for
// issue #79: a second `qvr sync` against a steady-state lockfile must
// emit exactly one line, "✓ Already in sync" (and nothing else) — no
// repeat scan banners, no re-linked symlink chatter, no auto-register
// notices. Matches the `uv sync` / `npm install` quiet idiom.
//
// We drive an edit-mode entry whose EditPath dir + SKILL.md are present
// so reconcile finds nothing to do — the only output should be the
// terminal success line.
func TestRunSync_NoOpRerun_StdoutIsSingleLine(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	if err := config.Save(&config.Config{}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	project := t.TempDir()
	t.Chdir(project)

	// Seed an edit-mode skill at its canonical EditPath so the reconciler
	// has nothing to repair. .claude/skills/demo doubles as both EditPath
	// (the canonical real dir) and the target for "claude" — reconciler
	// recognises this self-reference and skips the symlink pass.
	editRel := filepath.Join(".claude", "skills", "demo")
	editAbs := filepath.Join(project, editRel)
	if err := os.MkdirAll(editAbs, 0o755); err != nil {
		t.Fatalf("mkdir edit dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(editAbs, "SKILL.md"),
		[]byte("---\nname: demo\ndescription: noop test\n---\n# demo\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	// Real subtreeHash matching the on-disk content so VerifySingleEntry
	// reports OK (not Drift/Unverified) — otherwise sync's drift loop
	// suppresses "Already in sync" regardless of state.
	subtreeHash, err := canonical.HashSubtreeFromDisk(editAbs)
	if err != nil {
		t.Fatalf("hash subtree: %v", err)
	}

	lockPath := filepath.Join(project, model.LockFileName)
	lock := model.NewLockFile(lockPath)
	lock.Put(&model.LockEntry{
		Name:     "demo",
		Mode:     model.ModeEdit,
		EditPath: editRel,
		Source:   "https://example.com/demo.git",
		Ref:      "main",
		// Commit left empty so the verifier's commit-cross-check (issue #73)
		// is skipped — the edit dir has no .git/ in this fixture.
		SubtreeHash: subtreeHash,
		Targets:     []string{"claude"},
		InstalledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	// Reset package-level sync flags so a prior test's --strict / --dry-run
	// state doesn't bleed into this run.
	t.Cleanup(func() {
		syncGlobal = false
		syncDryRun = false
		syncKeepUntracked = false
		syncNoScan = false
		syncStrict = false
	})
	syncGlobal = false
	syncDryRun = false
	syncNoScan = true // sidestep the security scan path; it's silent on clean anyway but avoids any test-env oddity

	// First run: prime any state (config writes, scan attestation, etc.).
	withCapturingPrinter(t, "text")
	if err := runSync(syncCmd, nil); err != nil {
		t.Fatalf("first runSync: %v", err)
	}

	// Second run: capture stdout and assert it's exactly the no-op line.
	// The "✓ " prefix is added by Printer.Success.
	stdout := withCapturingPrinter(t, "text")
	if err := runSync(syncCmd, nil); err != nil {
		t.Fatalf("second runSync: %v", err)
	}
	got := stdout.String()
	want := "✓ Already in sync\n"
	if got != want {
		t.Errorf("no-op sync stdout = %q, want %q — issue #79: a steady-state rerun should emit one line, nothing else", got, want)
	}
}

// TestRunSync_Locked is the CI-contract guard for `qvr sync --locked`: an
// in-sync project passes (exit 0, lock untouched), and a stale lock (drift
// from recorded content) fails non-zero without mutating qvr.lock.
func TestRunSync_Locked(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	if err := config.Save(&config.Config{}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	project := t.TempDir()
	t.Chdir(project)

	editRel := filepath.Join(".claude", "skills", "demo")
	editAbs := filepath.Join(project, editRel)
	if err := os.MkdirAll(editAbs, 0o755); err != nil {
		t.Fatalf("mkdir edit dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(editAbs, "SKILL.md"),
		[]byte("---\nname: demo\ndescription: locked test\n---\n# demo\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	subtreeHash, err := canonical.HashSubtreeFromDisk(editAbs)
	if err != nil {
		t.Fatalf("hash subtree: %v", err)
	}

	lockPath := filepath.Join(project, model.LockFileName)
	writeLock := func(hash string) {
		lock := model.NewLockFile(lockPath)
		lock.Put(&model.LockEntry{
			Name:        "demo",
			Mode:        model.ModeEdit,
			EditPath:    editRel,
			Source:      "https://example.com/demo.git",
			Ref:         "main",
			SubtreeHash: hash,
			Targets:     []string{"claude"},
			InstalledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		})
		if err := lock.Write(); err != nil {
			t.Fatalf("write lock: %v", err)
		}
	}

	t.Cleanup(func() {
		syncGlobal, syncDryRun, syncKeepUntracked = false, false, false
		syncNoScan, syncStrict, syncAllowDrift, syncLocked = false, false, false, false
	})
	syncGlobal, syncDryRun, syncNoScan = false, false, true

	// In sync → --locked passes and leaves qvr.lock byte-identical.
	writeLock(subtreeHash)
	before, _ := os.ReadFile(lockPath)
	syncLocked = true
	withCapturingPrinter(t, "text")
	if err := runSync(syncCmd, nil); err != nil {
		t.Fatalf("sync --locked on an in-sync project failed: %v", err)
	}
	after, _ := os.ReadFile(lockPath)
	if !bytes.Equal(before, after) {
		t.Error("sync --locked rewrote qvr.lock on an in-sync project")
	}

	// Stale lock (recorded hash no longer matches on-disk content) → fail,
	// and the committed lock bytes are left untouched.
	writeLock("sha256:0000000000000000000000000000000000000000000000000000000000000000")
	stale, _ := os.ReadFile(lockPath)
	withCapturingPrinter(t, "text")
	if err := runSync(syncCmd, nil); err == nil {
		t.Error("sync --locked passed on a drifted lock; want non-zero exit")
	}
	restored, _ := os.ReadFile(lockPath)
	if !bytes.Equal(stale, restored) {
		t.Error("sync --locked mutated qvr.lock on failure; CI working tree should stay clean")
	}
}

// TestRunSync_JSONDrift_ExitsNonZero is the #125 guard: `qvr sync --output
// json` must honor the same exit-code contract as text mode. Drift fails the
// command by default, but the JSON branch used to return right after emitting
// the payload, so `--output json` exited 0 on a drifted lock — a CI script
// piping to `jq` saw success. The fix returns errJSONHandled (exit 1) after
// the payload, keeping stdout a single valid JSON document.
func TestRunSync_JSONDrift_ExitsNonZero(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	if err := config.Save(&config.Config{}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	project := t.TempDir()
	t.Chdir(project)

	editRel := filepath.Join(".claude", "skills", "demo")
	editAbs := filepath.Join(project, editRel)
	if err := os.MkdirAll(editAbs, 0o755); err != nil {
		t.Fatalf("mkdir edit dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(editAbs, "SKILL.md"),
		[]byte("---\nname: demo\ndescription: json drift test\n---\n# demo\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	lockPath := filepath.Join(project, model.LockFileName)
	lock := model.NewLockFile(lockPath)
	lock.Put(&model.LockEntry{
		Name:        "demo",
		Mode:        model.ModeEdit,
		EditPath:    editRel,
		Source:      "https://example.com/demo.git",
		Ref:         "main",
		SubtreeHash: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Targets:     []string{"claude"},
		InstalledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	t.Cleanup(func() {
		syncGlobal, syncDryRun, syncKeepUntracked = false, false, false
		syncNoScan, syncStrict, syncAllowDrift, syncLocked = false, false, false, false
	})
	syncGlobal, syncDryRun, syncKeepUntracked = false, false, false
	syncNoScan, syncStrict, syncAllowDrift, syncLocked = true, false, false, false

	stdout := withCapturingPrinter(t, "json")
	err := runSync(syncCmd, nil)
	if err == nil {
		t.Fatal("sync --output json returned nil on a drifted lock; want non-zero exit (#125)")
	}
	if !errors.Is(err, errJSONHandled) {
		t.Errorf("sync --output json drift error = %v, want errJSONHandled", err)
	}
	// stdout must still be a single well-formed JSON document (no diagnostic
	// lines mixed in) so a downstream `jq` doesn't choke.
	var payload any
	if jerr := json.Unmarshal(stdout.Bytes(), &payload); jerr != nil {
		t.Errorf("stdout is not valid JSON: %v\n%s", jerr, stdout.String())
	}
}

// TestRunSync_ReconcilerErrors_NonZeroExit is the cmd-boundary guard for
// issue #94: when the reconciler accumulates per-entry failures into
// result.Errors (printed individually via printer.Error), runSync must
// still return a non-nil error so cobra exits non-zero. Pre-fix runSync
// returned nil unconditionally and CI scripts ran the next step on a
// half-applied sync.
//
// We drive the failure through an edit-mode entry whose EditPath points
// at a directory that doesn't exist on disk — reconciler.restoreFromLock
// (internal/skill/reconciler.go:113) appends that to res.Errors without
// aborting the run. errTextHandled is the right return: the per-entry
// failure was already printed, the sentinel just promotes the exit code.
func TestRunSync_ReconcilerErrors_NonZeroExit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	if err := config.Save(&config.Config{}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	project := t.TempDir()
	t.Chdir(project)
	withCapturingPrinter(t, "text")

	// Reset package-level sync flags so a prior test's --strict/--dry-run
	// state doesn't bleed into this run.
	t.Cleanup(func() {
		syncGlobal = false
		syncDryRun = false
		syncKeepUntracked = false
		syncNoScan = false
		syncStrict = false
	})
	syncGlobal = false
	syncDryRun = false
	syncNoScan = true // skip the network-touching scan pass

	lockPath := filepath.Join(project, model.LockFileName)
	lock := model.NewLockFile(lockPath)
	lock.Put(&model.LockEntry{
		Name:        "ghost",
		Mode:        model.ModeEdit,
		EditPath:    ".claude/skills/ghost", // deliberately missing on disk
		Source:      "https://example.invalid/ghost.git",
		Ref:         "main",
		Commit:      "0000000000000000000000000000000000000000",
		Targets:     []string{"claude"},
		InstalledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	err := runSync(syncCmd, nil)
	if err == nil {
		t.Fatal("runSync returned nil despite reconciler errors — issue #94 regression")
	}
	if !errors.Is(err, errTextHandled) {
		t.Errorf("runSync returned %v, want errTextHandled (so Execute() exits 1 without duplicating the per-entry lines into a trailing Error envelope)", err)
	}
}

// Companion: a real (non-dry-run) sync still persists the new
// registry. Without this we'd risk a regression where the dryRun gate
// accidentally short-circuits both paths.
func TestAutoRegisterRegistriesFromLock_RealRunWritesConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	if err := config.Save(&config.Config{}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	withCapturingPrinter(t, "text")
	lock := model.NewLockFile(filepath.Join(t.TempDir(), model.LockFileName))
	lock.Put(&model.LockEntry{
		Name:     "demo",
		Registry: "raks",
		Source:   "git@github.com:raks097/skills.git",
		Ref:      "main",
		Commit:   "abc1234",
	})

	autoRegisterRegistriesFromLock(lock, false /*dryRun*/)

	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := loaded.Registries["raks"]
	if !ok {
		t.Fatalf("real run did not persist raks registry: %+v", loaded.Registries)
	}
	if got.URL != "git@github.com:raks097/skills.git" {
		t.Errorf("persisted URL = %q, want git@github.com:raks097/skills.git", got.URL)
	}
}

// TestRunSync_DriftFailsByDefault is the #118 regression. Pre-fix, sync
// printed a `!` drift line and exited 0 unless --strict was passed. An
// attacker who tampered ~/.quiver/worktrees/<sha>/ silently poisoned
// every project that next ran `qvr sync` without --strict. The fix flips
// the default — drift = error — and adds --allow-drift for the rare
// local-debug case.
//
// We drive an edit-mode entry with a SubtreeHash that intentionally
// doesn't match the on-disk content; VerifySingleEntry surfaces it as
// drift and runSync now returns a non-nil error.
func TestRunSync_DriftFailsByDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	if err := config.Save(&config.Config{}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	project := t.TempDir()
	t.Chdir(project)
	withCapturingPrinter(t, "text")

	t.Cleanup(func() {
		syncGlobal = false
		syncDryRun = false
		syncKeepUntracked = false
		syncNoScan = false
		syncStrict = false
		syncAllowDrift = false
	})
	syncNoScan = true

	editRel := filepath.Join(".claude", "skills", "demo")
	editAbs := filepath.Join(project, editRel)
	if err := os.MkdirAll(editAbs, 0o755); err != nil {
		t.Fatalf("mkdir edit dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(editAbs, "SKILL.md"),
		[]byte("---\nname: demo\ndescription: drift test\n---\n# demo\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	lockPath := filepath.Join(project, model.LockFileName)
	lock := model.NewLockFile(lockPath)
	lock.Put(&model.LockEntry{
		Name:        "demo",
		Mode:        model.ModeEdit,
		EditPath:    editRel,
		Source:      "https://example.invalid/demo.git",
		Ref:         "main",
		SubtreeHash: "sha256:not-the-real-hash", // forces VerifySingleEntry to report drift
		Targets:     []string{"claude"},
		InstalledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	err := runSync(syncCmd, nil)
	if err == nil {
		t.Fatal("runSync returned nil on subtreeHash drift — issue #118 regression: drift should fail by default")
	}
	if !strings.Contains(err.Error(), "integrity check") {
		t.Errorf("error = %v; want substring 'integrity check'", err)
	}
}

// TestRunSync_AllowDriftDowngrades pins the opt-out: --allow-drift
// keeps the pre-#118 warn-and-continue behaviour for the rare local
// debug case (the user wants to inspect the drift, not fail CI).
func TestRunSync_AllowDriftDowngrades(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	if err := config.Save(&config.Config{}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	project := t.TempDir()
	t.Chdir(project)
	withCapturingPrinter(t, "text")

	t.Cleanup(func() {
		syncGlobal = false
		syncDryRun = false
		syncKeepUntracked = false
		syncNoScan = false
		syncStrict = false
		syncAllowDrift = false
	})
	syncNoScan = true
	syncAllowDrift = true

	editRel := filepath.Join(".claude", "skills", "demo")
	editAbs := filepath.Join(project, editRel)
	if err := os.MkdirAll(editAbs, 0o755); err != nil {
		t.Fatalf("mkdir edit dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(editAbs, "SKILL.md"),
		[]byte("---\nname: demo\ndescription: drift opt-out\n---\n# demo\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	lockPath := filepath.Join(project, model.LockFileName)
	lock := model.NewLockFile(lockPath)
	lock.Put(&model.LockEntry{
		Name:        "demo",
		Mode:        model.ModeEdit,
		EditPath:    editRel,
		Source:      "https://example.invalid/demo.git",
		Ref:         "main",
		SubtreeHash: "sha256:not-the-real-hash",
		Targets:     []string{"claude"},
		InstalledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	if err := runSync(syncCmd, nil); err != nil {
		t.Errorf("runSync with --allow-drift returned %v, want nil — opt-out should keep the warn-and-continue path", err)
	}
}

// TestRunSync_UnverifiedIsSoft pins #154: an entry with NO recorded subtree
// hash (un-sealed, not tampered) must warn and exit 0 — the same verdict
// `qvr lock verify` gives — rather than hard-failing the way real drift does.
// Without --allow-drift. The on-disk content hashes fine; the only "problem"
// is the empty recorded hash, which `qvr lock upgrade` backfills.
func TestRunSync_UnverifiedIsSoft(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	if err := config.Save(&config.Config{}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	project := t.TempDir()
	t.Chdir(project)
	withCapturingPrinter(t, "text")

	t.Cleanup(func() {
		syncGlobal = false
		syncDryRun = false
		syncKeepUntracked = false
		syncNoScan = false
		syncStrict = false
		syncAllowDrift = false
	})
	syncNoScan = true

	editRel := filepath.Join(".claude", "skills", "demo")
	editAbs := filepath.Join(project, editRel)
	if err := os.MkdirAll(editAbs, 0o755); err != nil {
		t.Fatalf("mkdir edit dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(editAbs, "SKILL.md"),
		[]byte("---\nname: demo\ndescription: unverified test\n---\n# demo\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	lockPath := filepath.Join(project, model.LockFileName)
	lock := model.NewLockFile(lockPath)
	lock.Put(&model.LockEntry{
		Name:        "demo",
		Mode:        model.ModeEdit,
		EditPath:    editRel,
		Source:      "https://example.invalid/demo.git",
		Ref:         "main",
		SubtreeHash: "", // un-sealed: the #151/#154 state, NOT a hash mismatch
		Targets:     []string{"claude"},
		InstalledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	if err := runSync(syncCmd, nil); err != nil {
		t.Errorf("runSync returned %v on an un-sealed (empty-hash) entry; want nil — sync must match `qvr lock verify`'s soft treatment (#154)", err)
	}
}
