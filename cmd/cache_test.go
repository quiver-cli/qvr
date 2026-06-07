package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/astra-sh/qvr/internal/registry"
)

// resetPrinter wires the package-global `printer` to a discard sink for tests
// that call RunE functions directly.
func resetPrinter(t *testing.T) {
	t.Helper()
	prev := printer
	printer = &output.Printer{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, Format: output.FormatText}
	t.Cleanup(func() { printer = prev })
}

// fakeWorktree creates a directory under registry.WorktreesRoot() that looks
// like a real worktree to collectCacheEntries: a `.git` dir plus a payload
// file so dirSize > 0.
func fakeWorktree(t *testing.T, segments ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{registry.WorktreesRoot()}, segments...)...)
	if err := os.MkdirAll(filepath.Join(path, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	return path
}

// fakeContentDir creates a worktree-FREE content dir (post-#204 shape: the
// materialized skill tree with NO `.git` marker) under registry.WorktreesRoot().
// This is the install shape that the `.git`-marker walk could not see (#221).
func fakeContentDir(t *testing.T, segments ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{registry.WorktreesRoot()}, segments...)...)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir content dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	return path
}

// recordProjectWithLock writes a lock file at projectRoot/qvr.lock with a
// v5 entry whose EntryWorktreePath derivation (registry+name+
// ShortSHA(commit)) matches the path fakeWorktree(t, "acme", "demo",
// "abc1234") produces, then records the project so reachability sees it.
// Callers pass the seeded worktree path purely as a sanity hook — the
// derivation is authoritative.
func recordProjectWithLock(t *testing.T, projectRoot string) string {
	t.Helper()
	lockPath := filepath.Join(projectRoot, model.LockFileName)
	lock := model.NewLockFile(lockPath)
	lock.Put(&model.LockEntry{
		Name:     "demo",
		Registry: "acme",
		Source:   "git@example.test:acme.git",
		Ref:      "main",
		// The commit lines up with fakeWorktree(t, "acme", "demo",
		// "abc1234") so EntryWorktreePath resolves to the live worktree.
		Commit: "abc1234abcdef",
	})
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	registry.TouchProject(lockPath)
	return lockPath
}

func TestCollectCacheEntries_FlagsOrphansAndReachables(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)

	live := fakeWorktree(t, "acme", "demo", "abc1234")
	orphan := fakeWorktree(t, "acme", "demo", "deadbee")

	proj := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("mkdir proj: %v", err)
	}
	recordProjectWithLock(t, proj)

	entries, missing, err := collectCacheEntries()
	if err != nil {
		t.Fatalf("collectCacheEntries: %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("expected no missing projects, got %v", missing)
	}

	byPath := map[string]CacheEntry{}
	for _, e := range entries {
		byPath[e.Path] = e
	}
	if e, ok := byPath[live]; !ok || !e.Reachable {
		t.Errorf("live worktree not flagged reachable: %+v", e)
	}
	if e, ok := byPath[orphan]; !ok || e.Reachable {
		t.Errorf("orphan worktree not flagged orphan: %+v", e)
	}
}

func TestRunCachePrune_DryRunDoesNotDelete(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)

	orphan := fakeWorktree(t, "acme", "demo", "deadbee")
	_ = fakeWorktree(t, "acme", "demo", "abc1234") // live worktree the lock points at
	proj := filepath.Join(t.TempDir(), "proj")
	_ = os.MkdirAll(proj, 0o755)
	recordProjectWithLock(t, proj)

	// Wire up the printer for the test — runCachePrune touches it.
	resetPrinter(t)

	cachePruneDryRun = true
	t.Cleanup(func() { cachePruneDryRun = false })

	if err := runCachePrune(nil, nil); err != nil {
		t.Fatalf("runCachePrune dry-run: %v", err)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Errorf("dry-run should have left orphan in place: %v", err)
	}
}

func TestRunCachePrune_DeletesOrphans(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)

	orphan := fakeWorktree(t, "acme", "demo", "deadbee")
	live := fakeWorktree(t, "acme", "demo", "abc1234")
	proj := filepath.Join(t.TempDir(), "proj")
	_ = os.MkdirAll(proj, 0o755)
	recordProjectWithLock(t, proj)

	resetPrinter(t)
	cachePruneDryRun = false
	// v0.8.7 (#110) gate: explicit consent required for the destructive
	// real-run when stdin isn't a TTY. `go test` runs with non-TTY
	// stdin so we'd hit the refusal without --yes.
	cachePruneYes = true
	t.Cleanup(func() { cachePruneYes = false })

	if err := runCachePrune(nil, nil); err != nil {
		t.Fatalf("runCachePrune: %v", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("orphan should be gone, got err=%v", err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("reachable worktree should survive, got err=%v", err)
	}
}

// TestRunCachePrune_ReturnsErrorOnDeleteFailure pins the contract that a
// partial-failure prune does NOT exit 0 — a CI script wrapping `qvr cache
// prune` must be able to detect that some orphans couldn't be removed.
// Achieved by making one of the orphan paths unremovable: chmod the parent
// dir to read-only so RemoveAll fails on the leaf.
func TestRunCachePrune_ReturnsErrorOnDeleteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses chmod permissions")
	}
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)

	// Build an orphan inside a parent we'll lock down.
	parent := filepath.Join(home, "worktrees", "acme", "demo")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	orphan := fakeWorktree(t, "acme", "demo", "deadbee")
	// Make parent read-only so RemoveAll(orphan) fails — leaf still
	// readable, but rmdir of the leaf needs write+exec on parent.
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatalf("chmod parent: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	resetPrinter(t)
	cachePruneDryRun = false
	cachePruneYes = true
	t.Cleanup(func() { cachePruneYes = false })
	err := runCachePrune(nil, nil)
	if err == nil {
		t.Fatal("expected error on delete failure, got nil")
	}
	// The orphan should still be on disk (delete failed).
	if _, statErr := os.Stat(orphan); statErr != nil {
		t.Fatalf("orphan unexpectedly removed even though delete failed: %v", statErr)
	}
}

// TestRunCachePrune_NonInteractiveRefusesWithoutYes guards issue #110:
// off a TTY (CI, pipelines) and without --yes, prune must refuse rather
// than silently delete. Forces the non-TTY path via the stdinIsTTYFn
// seam (real test runners can themselves be attached to a TTY).
func TestRunCachePrune_NonInteractiveRefusesWithoutYes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)

	orphan := fakeWorktree(t, "acme", "demo", "deadbee")
	_ = fakeWorktree(t, "acme", "demo", "abc1234")
	proj := filepath.Join(t.TempDir(), "proj")
	_ = os.MkdirAll(proj, 0o755)
	recordProjectWithLock(t, proj)

	resetPrinter(t)
	cachePruneDryRun = false
	cachePruneYes = false
	prevTTY := stdinIsTTYFn
	stdinIsTTYFn = func() bool { return false }
	t.Cleanup(func() { stdinIsTTYFn = prevTTY })

	err := runCachePrune(nil, nil)
	if err == nil {
		t.Fatal("expected non-interactive refusal without --yes, got nil")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("error should hint at --yes, got %v", err)
	}
	// Critically: the orphan must still exist — refusal means no
	// destructive op ran.
	if _, statErr := os.Stat(orphan); statErr != nil {
		t.Errorf("orphan was deleted despite refusal: %v", statErr)
	}
}

// TestRunCachePrune_VanishedProjectReclaimsWorktreeFreeContentDir is the #221
// regression guard: a worktree-free content dir (#204) orphaned by a vanished
// project must be reclaimed by `cache prune --yes` (and listed by --dry-run),
// not leaked permanently. Before the fix, the `.git`-marker walk never saw a
// content dir, so prune reported "Removed 0 worktree(s)" and the dir survived
// every subsequent run.
func TestRunCachePrune_VanishedProjectReclaimsWorktreeFreeContentDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)

	// The worktree-free enumerator keys on configured registries, so the
	// registry must be present in config (only the PROJECT vanishes, not the
	// registry — matching the reported repro).
	if err := config.Save(&config.Config{
		Registries: map[string]config.RegistryConfig{
			"acme": {URL: "git@example.test:acme.git"},
		},
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	// A worktree-free content dir (no .git) the vanished project uniquely
	// referenced. The commit lines up with WorktreePath(acme, demo, abc1234).
	content := fakeContentDir(t, "acme", "demo", "abc1234")

	deadProj := filepath.Join(t.TempDir(), "dead")
	deadLock := filepath.Join(deadProj, model.LockFileName)
	if err := os.MkdirAll(deadProj, 0o755); err != nil {
		t.Fatalf("mkdir dead project: %v", err)
	}
	dl := model.NewLockFile(deadLock)
	dl.Put(&model.LockEntry{
		Name:     "demo",
		Registry: "acme",
		Source:   "git@example.test:acme.git",
		Ref:      "main",
		Commit:   "abc1234abcdef",
	})
	if err := dl.Write(); err != nil {
		t.Fatalf("write dead lock: %v", err)
	}
	registry.TouchProject(deadLock)
	_ = os.RemoveAll(deadProj) // user rm -rf'd the project → worktree orphaned

	resetPrinter(t)
	cachePruneDryRun = false
	cachePruneYes = true
	t.Cleanup(func() { cachePruneYes = false })

	if err := runCachePrune(nil, nil); err != nil {
		t.Fatalf("runCachePrune: %v", err)
	}

	if _, err := os.Stat(content); !os.IsNotExist(err) {
		t.Errorf("LEAK: worktree-free content dir was not reclaimed (stat err=%v)", err)
	}
	pf, _ := registry.ReadProjects()
	if _, ok := pf.Projects[deadLock]; ok {
		t.Errorf("vanished project should have been forgotten")
	}

	// Permanent-leak guard: a second prune finds nothing left and doesn't error.
	resetPrinter(t)
	if err := runCachePrune(nil, nil); err != nil {
		t.Fatalf("second runCachePrune: %v", err)
	}
}

func TestRunCachePrune_ForgetsVanishedProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	_ = fakeWorktree(t, "acme", "demo", "abc1234") // live worktree the lock points at
	proj := filepath.Join(t.TempDir(), "proj")
	_ = os.MkdirAll(proj, 0o755)
	livePath := recordProjectWithLock(t, proj)

	deadProj := filepath.Join(t.TempDir(), "dead")
	deadLock := filepath.Join(deadProj, model.LockFileName)
	// Record a project lock whose directory we then delete to simulate the
	// "user rm-rf'd the project" case.
	_ = os.MkdirAll(deadProj, 0o755)
	deadLockFile := model.NewLockFile(deadLock)
	if err := deadLockFile.Write(); err != nil {
		t.Fatalf("write dead lock: %v", err)
	}
	registry.TouchProject(deadLock)
	_ = os.RemoveAll(deadProj)

	resetPrinter(t)
	cachePruneDryRun = false
	cachePruneYes = true
	t.Cleanup(func() { cachePruneYes = false })
	if err := runCachePrune(nil, nil); err != nil {
		t.Fatalf("runCachePrune: %v", err)
	}

	pf, _ := registry.ReadProjects()
	if _, ok := pf.Projects[deadLock]; ok {
		t.Errorf("dead project should have been forgotten")
	}
	if _, ok := pf.Projects[livePath]; !ok {
		t.Errorf("live project should still be recorded")
	}
}

// TestCacheCmd_UnknownSubcommandErrors is the #120 regression: pre-fix
// `qvr cache <typo>` (any non-list/prune/clean) silently printed the parent
// help and exited 0, so a CI script with a typo would look like it
// succeeded. The fix mirrors lockCmd's RunE so an unknown positional
// returns an "unknown command" error. (`clean` is now a real verb, so the
// probe uses an unmistakably-unknown token.)
func TestCacheCmd_UnknownSubcommandErrors(t *testing.T) {
	err := cacheCmd.RunE(cacheCmd, []string{"bogus"})
	if err == nil {
		t.Fatal("cacheCmd.RunE returned nil on unknown subcommand 'bogus'")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("error = %v; want substring 'unknown command'", err)
	}
}

// TestRunCachePrune_DryRunUsesWouldRemoveField is the #122 regression.
// Pre-fix dry-run populated `Removed` and `FreedBytes` — the same names
// the destructive run uses — so a scriptable consumer reading those
// fields after a dry-run thought the prune ran. Now dry-run writes to
// WouldRemove/WouldFree (with omitempty so the destructive fields stay
// out of the JSON entirely).
func TestRunCachePrune_DryRunUsesWouldRemoveField(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	orphan := fakeWorktree(t, "acme", "demo", "deadbee")
	_ = fakeWorktree(t, "acme", "demo", "abc1234") // live worktree the lock points at
	proj := filepath.Join(t.TempDir(), "proj")
	_ = os.MkdirAll(proj, 0o755)
	recordProjectWithLock(t, proj)

	resetPrinter(t)
	cachePruneDryRun = true
	t.Cleanup(func() { cachePruneDryRun = false })

	if err := runCachePrune(nil, nil); err != nil {
		t.Fatalf("runCachePrune dry-run: %v", err)
	}
	// Sanity-check via stat that nothing was deleted (dry-run contract).
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("dry-run deleted orphan: %v", err)
	}

	// Now drive the JSON-emitting path so we can inspect the field shape.
	printer = &output.Printer{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, Format: output.FormatJSON}
	if err := runCachePrune(nil, nil); err != nil {
		t.Fatalf("runCachePrune dry-run json: %v", err)
	}
	outBuf, ok := printer.Out.(*bytes.Buffer)
	if !ok {
		t.Fatalf("printer.Out is not a *bytes.Buffer; got %T", printer.Out)
	}
	body := outBuf.String()
	if !strings.Contains(body, "\"wouldRemove\"") {
		t.Errorf("dry-run JSON missing wouldRemove field — issue #122:\n%s", body)
	}
	if !strings.Contains(body, "\"wouldFree\"") {
		t.Errorf("dry-run JSON missing wouldFree field — issue #122:\n%s", body)
	}
	if strings.Contains(body, "\"removed\"") {
		t.Errorf("dry-run JSON should NOT emit 'removed' (omitempty + write to WouldRemove) — issue #122:\n%s", body)
	}
}

// resetCacheCleanFlags restores the package-global clean flags after a test
// mutates them, so cases don't leak state into each other.
func resetCacheCleanFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		cacheCleanDryRun = false
		cacheCleanYes = false
		cacheCleanRegistries = false
	})
}

// TestRunCacheClean_DryRunListsAllAndDeletesNothing pins clean's dry-run
// contract: it enumerates BOTH reachable and orphan worktrees (unlike prune)
// and touches nothing on disk.
func TestRunCacheClean_DryRunListsAllAndDeletesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)

	live := fakeWorktree(t, "acme", "demo", "abc1234")
	orphan := fakeWorktree(t, "acme", "demo", "deadbee")
	proj := filepath.Join(t.TempDir(), "proj")
	_ = os.MkdirAll(proj, 0o755)
	recordProjectWithLock(t, proj)

	resetPrinter(t)
	resetCacheCleanFlags(t)
	cacheCleanDryRun = true

	if err := runCacheClean(nil, nil); err != nil {
		t.Fatalf("runCacheClean dry-run: %v", err)
	}
	// Both the reachable and the orphan worktree must survive a dry-run.
	if _, err := os.Stat(live); err != nil {
		t.Errorf("dry-run deleted reachable worktree: %v", err)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Errorf("dry-run deleted orphan worktree: %v", err)
	}
}

// TestRunCacheClean_DryRunUsesWouldRemoveField mirrors the #122 field-split
// contract for clean: dry-run populates wouldRemove/wouldFree, never
// removed/freedBytes, so a consumer can't mistake a preview for a real wipe.
func TestRunCacheClean_DryRunUsesWouldRemoveField(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	_ = fakeWorktree(t, "acme", "demo", "deadbee")

	resetPrinter(t) // registers restoration of the global printer
	resetCacheCleanFlags(t)
	cacheCleanDryRun = true

	printer = &output.Printer{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, Format: output.FormatJSON}
	if err := runCacheClean(nil, nil); err != nil {
		t.Fatalf("runCacheClean dry-run json: %v", err)
	}
	outBuf, ok := printer.Out.(*bytes.Buffer)
	if !ok {
		t.Fatalf("printer.Out is not a *bytes.Buffer; got %T", printer.Out)
	}
	body := outBuf.String()
	if !strings.Contains(body, "\"wouldRemove\"") || !strings.Contains(body, "\"wouldFree\"") {
		t.Errorf("dry-run JSON missing wouldRemove/wouldFree:\n%s", body)
	}
	if strings.Contains(body, "\"removed\"") {
		t.Errorf("dry-run JSON must not emit 'removed':\n%s", body)
	}
}

// TestRunCacheClean_WipesReachableAndOrphan is the core difference from prune:
// clean removes EVERY worktree, including the ones a live lock points at, plus
// the index cache. Bare registry clones stay put by default.
func TestRunCacheClean_WipesReachableAndOrphan(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)

	live := fakeWorktree(t, "acme", "demo", "abc1234")
	orphan := fakeWorktree(t, "acme", "demo", "deadbee")
	proj := filepath.Join(t.TempDir(), "proj")
	_ = os.MkdirAll(proj, 0o755)
	recordProjectWithLock(t, proj)

	// Seed an index cache and a bare-clone dir to assert clean's default scope.
	idx := registry.CacheDir()
	_ = os.MkdirAll(idx, 0o755)
	_ = os.WriteFile(filepath.Join(idx, "acme.json"), []byte("{}"), 0o644)
	registriesDir := filepath.Join(home, "registries")
	_ = os.MkdirAll(registriesDir, 0o755)
	_ = os.WriteFile(filepath.Join(registriesDir, "marker"), []byte("x"), 0o644)

	resetPrinter(t)
	resetCacheCleanFlags(t)
	cacheCleanYes = true // non-TTY consent

	if err := runCacheClean(nil, nil); err != nil {
		t.Fatalf("runCacheClean: %v", err)
	}
	for _, p := range []string{live, orphan, idx} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected %s gone, stat err=%v", p, err)
		}
	}
	// Registries survive without --registries.
	if _, err := os.Stat(registriesDir); err != nil {
		t.Errorf("bare registries should survive a default clean: %v", err)
	}
}

// TestRunCacheClean_RegistriesFlagAlsoDropsBareClones asserts --registries
// extends the wipe to ~/.quiver/registries/.
func TestRunCacheClean_RegistriesFlagAlsoDropsBareClones(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	_ = fakeWorktree(t, "acme", "demo", "deadbee")
	registriesDir := filepath.Join(home, "registries")
	_ = os.MkdirAll(registriesDir, 0o755)
	_ = os.WriteFile(filepath.Join(registriesDir, "marker"), []byte("x"), 0o644)

	resetPrinter(t)
	resetCacheCleanFlags(t)
	cacheCleanYes = true
	cacheCleanRegistries = true

	if err := runCacheClean(nil, nil); err != nil {
		t.Fatalf("runCacheClean --registries: %v", err)
	}
	if _, err := os.Stat(registriesDir); !os.IsNotExist(err) {
		t.Errorf("--registries should remove bare clones, stat err=%v", err)
	}
}

// TestRunCacheClean_NonInteractiveRefusesWithoutYes guards the destructive
// gate: off a TTY and without --yes, clean refuses and deletes nothing.
func TestRunCacheClean_NonInteractiveRefusesWithoutYes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	live := fakeWorktree(t, "acme", "demo", "abc1234")

	resetPrinter(t)
	resetCacheCleanFlags(t)
	prevTTY := stdinIsTTYFn
	stdinIsTTYFn = func() bool { return false }
	t.Cleanup(func() { stdinIsTTYFn = prevTTY })

	err := runCacheClean(nil, nil)
	if err == nil {
		t.Fatal("expected non-interactive refusal without --yes, got nil")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("error should hint at --yes, got %v", err)
	}
	if _, statErr := os.Stat(live); statErr != nil {
		t.Errorf("worktree deleted despite refusal: %v", statErr)
	}
}

// TestRunCacheClean_JSONRefusalIsNonZero pins that under --output json the
// missing-consent path is still a hard error (not a body with exit 0).
func TestRunCacheClean_JSONRefusalIsNonZero(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	_ = fakeWorktree(t, "acme", "demo", "deadbee")

	resetPrinter(t) // registers restoration of the global printer
	resetCacheCleanFlags(t)
	printer = &output.Printer{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, Format: output.FormatJSON}

	if err := runCacheClean(nil, nil); err == nil {
		t.Fatal("expected JSON-mode refusal without --yes, got nil")
	}
}

// TestRunCacheList_LowercaseOrphan is the #122 regression for the
// ORPHAN-vs-reachable case mismatch. Both row states should be
// lowercase so the column reads cleanly.
func TestRunCacheList_LowercaseOrphan(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	_ = fakeWorktree(t, "acme", "demo", "deadbee") // orphan

	buf := &bytes.Buffer{}
	prev := printer
	printer = &output.Printer{Out: buf, Err: &bytes.Buffer{}, Format: output.FormatText}
	t.Cleanup(func() { printer = prev })

	if err := runCacheList(nil, nil); err != nil {
		t.Fatalf("runCacheList: %v", err)
	}
	got := buf.String()
	if strings.Contains(got, "ORPHAN") {
		t.Errorf("cache list still uses uppercase ORPHAN — issue #122:\n%s", got)
	}
	if !strings.Contains(got, "orphan") {
		t.Errorf("cache list missing lowercase orphan row:\n%s", got)
	}
}
