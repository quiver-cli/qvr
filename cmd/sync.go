package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/git"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/astra-sh/qvr/internal/registry"
	"github.com/astra-sh/qvr/internal/security"
	"github.com/astra-sh/qvr/internal/skill"
	"github.com/spf13/cobra"
)

var (
	syncGlobal        bool
	syncDryRun        bool
	syncKeepUntracked bool
	syncNoScan        bool
	syncStrict        bool
	syncAllowDrift    bool
	syncLocked        bool
	syncFrozen        bool
	syncCheck         bool
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Reconcile the project against qvr.lock",
	Long: `Make the on-disk state match the lock file. For every entry in the
lock, ensure its worktree exists in the shared cache and the agent-target
symlinks point at it. Then strict-remove any symlinks under managed agent
directories (.claude/skills/, .cursor/rules/, etc.) whose target is a
qvr-managed cache path but which don't appear in the lock — that's the
"hidden by default" guarantee.

A symlink whose target sits outside the qvr-managed scope (e.g. into your
own dev directory or somewhere weirder like /etc/passwd) is left alone
and surfaced in the output so you can investigate; sync never removes
anything we don't recognise as ours.

Pass --global to reconcile against the user-global lock at ~/.quiver/qvr.lock.
Pass --dry-run to see what would change without touching the filesystem.
Pass --keep-untracked to downgrade orphan removal to a warning — handy
when you mix hand-managed skills with qvr-managed ones in the same dir.`,
	RunE: runSync,
}

func init() {
	syncCmd.Flags().BoolVar(&syncGlobal, "global", false,
		"reconcile against the user-global lock instead of the project lock")
	syncCmd.Flags().BoolVar(&syncDryRun, "dry-run", false,
		"report what would change without touching the filesystem")
	syncCmd.Flags().BoolVar(&syncKeepUntracked, "keep-untracked", false,
		"warn about orphan managed symlinks instead of removing them")
	syncCmd.Flags().BoolVar(&syncNoScan, "no-scan", false,
		"skip the per-skill security scan that normally surfaces issues found in restored worktrees")
	// --strict is now the default and kept as a no-op alias for backward
	// compatibility with CI scripts that already pass it. Hidden from the
	// help text to discourage new usage. Issue #118.
	syncCmd.Flags().BoolVar(&syncStrict, "strict", false,
		"deprecated: drift now fails by default (kept for back-compat)")
	_ = syncCmd.Flags().MarkHidden("strict")
	syncCmd.Flags().BoolVar(&syncAllowDrift, "allow-drift", false,
		"downgrade subtreeHash drift from an error to a warning (rare local-debug case; CI should never set this)")
	syncCmd.Flags().BoolVar(&syncLocked, "locked", false,
		"CI assertion: restore worktrees but make NO changes — exit non-zero if a sync would modify qvr.lock (stale/incomplete lock) or if any skill drifted")
	syncCmd.Flags().BoolVar(&syncFrozen, "frozen", false,
		"restore strictly from the lock as-is: never resolve or rewrite qvr.lock, and tolerate a stale lock rather than failing (like uv sync --frozen)")
	syncCmd.Flags().BoolVar(&syncCheck, "check", false,
		"read-only CI assertion: report whether the project is in sync and exit non-zero if not, without writing anything (not even transiently)")
	rootCmd.AddCommand(syncCmd)
}

func runSync(cmd *cobra.Command, args []string) error {
	// --locked / --frozen / --check are three distinct CI/restore modes; mixing
	// them is a contradiction (assert-unchanged vs tolerate-stale vs read-only).
	// Reject up front so a script can't silently get one mode's behaviour while
	// believing it asked for another.
	if n := boolCount(syncLocked, syncFrozen, syncCheck); n > 1 {
		return fmt.Errorf("--locked, --frozen, and --check are mutually exclusive; pass at most one")
	}

	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), syncGlobal)
	projPath := model.DefaultProjectPath(projectRoot)
	cfg, cerr := config.Load()
	if cerr != nil {
		return fmt.Errorf("load config: %w", cerr)
	}
	if err := enforceScanPolicy(cfg, syncNoScan); err != nil {
		return err
	}

	// --locked is a CI assertion: a sync must not modify qvr.lock. Snapshot the
	// committed bytes up front; after reconcile we compare and fail (restoring
	// the snapshot) if anything would have changed. Auto-register and scan
	// persistence are suppressed under --locked so the only thing that can
	// rewrite the lock is the reconciler restoring a genuinely stale entry.
	// Both --locked and --frozen guarantee qvr.lock bytes don't change: --locked
	// fails if they would, --frozen silently restores the snapshot and carries
	// on. Snapshot up front so we can compare/restore after reconcile.
	var lockedPrior []byte
	if syncLocked || syncFrozen {
		lockedPrior, _ = os.ReadFile(lockPath)
	}

	var sr syncReconcileResult
	lockErr := model.WithLock(config.Dir(), lockPath, func() error {
		return runSyncReconcile(cmd, cfg, projectRoot, projPath, lockPath, &sr)
	})
	if lockErr != nil {
		return lockErr
	}
	result := sr.result
	latestLock := sr.latestLock
	atOrAboveThreshold := sr.atOrAboveThreshold
	driftReports := sr.driftReports
	unverifiedReports := sr.unverifiedReports
	projWarnings := sr.projWarnings
	projWouldChange := sr.projWouldChange

	// --locked / --frozen lock-byte guarantee: the reconciler may have rewritten
	// qvr.lock while restoring a stale entry. If the on-disk lock now differs
	// from what was committed, restore the committed bytes (keep CI's tree
	// clean). --locked then FAILS — a byte-identical lock means a real sync
	// would have been a no-op, which is what it asserts. --frozen does NOT fail:
	// it intentionally tolerates a stale lock, having just restored worktrees
	// from it without updating it.
	if err := enforceLockByteGuarantee(lockPath, lockedPrior); err != nil {
		return err
	}

	syncPostReconcileBookkeeping(projectRoot, lockPath, latestLock)

	checkFailed := syncCheckFailed(result, driftReports, projWouldChange)

	if printer.Format == output.FormatJSON {
		return emitSyncJSON(result, driftReports, checkFailed)
	}

	// qvr.toml reconciliation advisories (ref-conflict "lock wins", install
	// failures for hand-added skills) print before the reconcile summary so the
	// user sees intent-level notes first.
	renderSyncSummary(result, atOrAboveThreshold, driftReports, unverifiedReports, projWarnings)
	renderSyncCleanVerdict(&sr)
	if err := syncTextExitCode(result, driftReports, checkFailed); err != nil {
		return err
	}
	// Orphan-worktree hint. After a normal sync, surface accumulated
	// orphans in the shared cache so users see them without having to
	// know `qvr cache list` exists. Threshold-gated (5+ orphans OR
	// 100 MB+) so a single stray worktree doesn't nag every run.
	// OSS-readiness finding: ~49 orphans (~200 MB) had accumulated
	// across normal use without any prompt to clean.
	if !syncDryRun && !syncCheck {
		printOrphanHintIfBig()
	}
	return nil
}

// syncPostReconcileBookkeeping performs the benign disk-touching side effects
// after a successful reconcile: the projects.json bookkeeping touch and the
// opt-in AGENTS.md refresh. Both are skipped under --check (read-only assertion
// with zero side effects); the AGENTS.md refresh is further gated on a project
// (non-global), non-dry-run run with a lock to read from.
func syncPostReconcileBookkeeping(projectRoot, lockPath string, latestLock *model.LockFile) {
	// --check is read-only: skip the projects.json bookkeeping touch and the
	// AGENTS.md refresh (both write to disk) so the assertion has zero side
	// effects, even the benign ones.
	if !syncCheck {
		registry.TouchProject(lockPath)
	}

	// Refresh AGENTS.md if the user has opted in (file already present). The
	// reconciler may have changed which skills are visible, so the doc cache
	// can otherwise lie until the next manual `qvr docs`.
	if !syncGlobal && !syncDryRun && !syncCheck && latestLock != nil {
		_ = refreshAgentsMDIfPresent(projectRoot, latestLock.Entries())
	}
}

// syncCheckFailed reports the --check verdict: anything a real sync would change
// — a worktree to restore, a symlink to (re)create, an orphan to remove, drift,
// or a reconcile error — means the project is NOT in sync with qvr.lock. The
// reconcile ran in dry-run, so these are all "would" findings and nothing was
// mutated; the caller only translates the verdict into a non-zero exit.
func syncCheckFailed(result *skill.ReconcileResult, driftReports []skill.VerifyEntryResult, projWouldChange int) bool {
	return syncCheck && (len(result.Installed) > 0 || len(result.SymlinksFixed) > 0 ||
		len(result.Removed) > 0 || len(driftReports) > 0 || len(result.Errors) > 0 ||
		projWouldChange > 0)
}

// renderSyncCleanVerdict prints the "Already in sync" / "In sync with qvr.lock"
// success line when the reconcile found nothing to do and no findings surfaced.
func renderSyncCleanVerdict(sr *syncReconcileResult) {
	result := sr.result
	if len(result.Installed)+len(result.SymlinksFixed)+len(result.Removed) == 0 && len(result.Errors) == 0 && len(sr.atOrAboveThreshold) == 0 && len(sr.driftReports) == 0 && len(sr.unverifiedReports) == 0 && len(sr.projWarnings) == 0 && sr.projWouldChange == 0 {
		if syncCheck {
			printer.Success("In sync with qvr.lock")
		} else {
			printer.Success("Already in sync")
		}
	}
}

// syncReconcileResult carries the outputs of the under-lock reconcile pass
// (runSyncReconcile) back to runSync for rendering and exit-code decisions.
type syncReconcileResult struct {
	result             *skill.ReconcileResult
	latestLock         *model.LockFile
	atOrAboveThreshold map[string]security.Severity
	driftReports       []skill.VerifyEntryResult
	// unverifiedReports is kept separate from driftReports: an entry with no
	// recorded subtree hash is "un-sealed", not tampered. `qvr lock verify`
	// surfaces it as a soft warning (exit 0), so sync must agree rather than
	// hard-fail the identical state (#154). It renders as a warning and is
	// remediated by `qvr lock upgrade`, but never flips the exit code.
	unverifiedReports []skill.VerifyEntryResult
	// projWarnings/projWouldChange come from the qvr.toml → lock pre-pass:
	// declarative intent applied before reconcile. wouldChange counts skills
	// qvr.toml declares that the lock lacks — a stale lock that flips --check.
	projWarnings    []string
	projWouldChange int
}

// runSyncReconcile performs the under-lock body of sync: auto-register missing
// registries, apply the qvr.toml pre-pass, reconcile worktrees/symlinks, then
// (non-dry-run) rescan restored skills and verify subtree-hash drift. Results
// are accumulated into sr for the caller to render and key exit codes on. Runs
// inside WithLock so the lock.Write() in scanAndPersistRestored persists scan
// signals atomically with reconciler-side changes.
func runSyncReconcile(cmd *cobra.Command, cfg *config.Config, projectRoot, projPath, lockPath string, sr *syncReconcileResult) error {
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return fmt.Errorf("read lock: %w", err)
	}

	// Self-healing registry config: a fresh clone of a project may
	// have qvr.lock entries that reference registries this user
	// hasn't registered yet. v5's `source` field carries the fetch
	// URL on every non-link entry, so we can auto-register the
	// missing ones from the lock — letting `qvr sync` succeed on a
	// fresh checkout without making the user re-add registries by
	// hand.
	//
	// Under --dry-run we still report what *would* be registered so
	// the user gets the full picture of pending state changes, but
	// we never call config.Save — dry-run's contract is no
	// filesystem mutations.
	// Under --locked/--frozen/--check, run auto-register in dry-run so it
	// never persists config — those modes forbid (or freeze) side effects.
	autoRegisterRegistriesFromLock(lock, syncDryRun || syncLocked || syncFrozen || syncCheck)

	gc := git.NewGoGitClient()
	wt := git.NewGoGitWorktree()
	installer := skill.NewInstaller(newRegistryManager(gc), wt, gc)
	reconciler := skill.NewReconciler(installer)

	// qvr.toml → lock pre-pass: apply declarative intent BEFORE reconcile so
	// case-C (a skill qvr.toml declares but the lock lacks) is resolved and
	// installed into the lock, then materialised by the reconcile below. The
	// lock stays authoritative: a ref that disagrees with qvr.toml leaves the
	// lock as-is (warn). --frozen ignores qvr.toml entirely; --global has no
	// project file. Writes (install + synthesis) are gated by `apply`.
	if !syncGlobal && !syncFrozen {
		apply := !syncDryRun && !syncLocked && !syncCheck
		sr.projWarnings, sr.projWouldChange = applyProjectFileToLock(
			projPath, lock, installer, cfg, projectRoot, lockPath, apply)
	}

	// --check is read-only: run the reconcile in dry-run so it reports what
	// WOULD change (restores, symlinks, orphans) without mutating disk or
	// the lock. The would-change lists then drive --check's exit code.
	r, err := reconciler.Reconcile(lock, projectRoot, config.Dir(), skill.ReconcileOptions{
		DryRun:                   syncDryRun || syncCheck,
		RequireSigned:            cfg.Security.RequireSigned,
		TrustedAuthorsByRegistry: trustedAuthorsByRegistry(cfg),
		KeepUntracked:            syncKeepUntracked,
	})
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	sr.result = r
	sr.latestLock = lock

	// Security gate. Sync re-materialises worktrees from the lock;
	// we rescan each restored skill so a registry that turned
	// hostile between add and sync gets flagged. Sync only surfaces
	// findings and does not roll back — the lock already committed
	// to these refs and the user can `qvr remove` individually
	// after reviewing what the scan said.
	//
	// Runs inside the lock window because scanRestoredSkillsAfterSync
	// writes the scan signal into entry.Verification.Scan on the
	// in-memory lock. The lock.Write() that follows persists those
	// mutations alongside any reconciler-side changes; both must be
	// inside WithLock so concurrent qvr commands see a consistent
	// snapshot.
	if !syncDryRun {
		if err := scanAndVerifyRestored(cmd, cfg, projectRoot, lockPath, lock, sr); err != nil {
			return err
		}
	}
	return nil
}

// scanAndVerifyRestored runs the non-dry-run post-reconcile gate: rescan
// restored skills (persisting findings, unless a CI mode forbids it) and verify
// subtree-hash/commit drift. Results land on sr for the caller to render.
func scanAndVerifyRestored(cmd *cobra.Command, cfg *config.Config, projectRoot, lockPath string, lock *model.LockFile, sr *syncReconcileResult) error {
	// Under --locked/--frozen/--check we skip the rescan + scan-result
	// persist: those modes forbid (or freeze) mutating qvr.lock, and
	// rescanning would rewrite verification.scan. Drift verification
	// below still runs for all of them.
	if !syncLocked && !syncFrozen && !syncCheck {
		flagged, serr := scanAndPersistRestored(cmd.Context(), lock, cfg, projectRoot, lockPath)
		if serr != nil {
			return serr
		}
		sr.atOrAboveThreshold = flagged
	}

	// Subtree-hash drift check (issue #65). The reconciler restored
	// every worktree from the lock's recorded commit, but it does
	// not verify the on-disk content matches the lock's recorded
	// SubtreeHash. A stale, corrupted, or tampered entry passes
	// reconcile cleanly and only surfaces on the next
	// `qvr lock verify`. Run that verification here so sync is the
	// integrity gate the user expects: warn on drift by default,
	// promote to a non-zero exit under --strict.
	sr.driftReports, sr.unverifiedReports = verifySyncDrift(lock, projectRoot)
	return nil
}

// enforceLockByteGuarantee implements the --locked/--frozen lock-byte contract:
// if the reconciler rewrote qvr.lock while restoring a stale entry, restore the
// committed bytes (keep CI's tree clean). --locked then fails (a byte-identical
// lock means a real sync would have been a no-op); --frozen tolerates it.
func enforceLockByteGuarantee(lockPath string, lockedPrior []byte) error {
	if !syncLocked && !syncFrozen {
		return nil
	}
	finalBytes, _ := os.ReadFile(lockPath)
	if !bytes.Equal(lockedPrior, finalBytes) {
		if len(lockedPrior) > 0 {
			_ = os.WriteFile(lockPath, lockedPrior, 0o644)
		}
		if syncLocked {
			return fmt.Errorf("sync --locked: qvr.lock is out of date — a sync would modify it; run `qvr sync` (or `qvr lock upgrade`) and commit the result")
		}
	}
	return nil
}

// syncTextExitCode applies the text-mode exit-code contract after the summary
// has been rendered: --check would-change failures, drift (unless --frozen /
// --allow-drift), and per-entry reconcile errors each flip the exit non-zero.
func syncTextExitCode(result *skill.ReconcileResult, driftReports []skill.VerifyEntryResult, checkFailed bool) error {
	// --check verdict (read-only): any would-change finding fails the assertion.
	// The individual would-install / would-create / drift lines already printed
	// above; this is the single non-zero exit a CI gate keys on.
	if checkFailed {
		return fmt.Errorf("sync --check: project is not in sync with qvr.lock — run `qvr sync` and commit the result")
	}
	// Drift defaults to a non-zero exit (issue #118). Pre-fix, drift only
	// failed under --strict, so a CI script running `qvr sync` could never
	// catch a tampered cached worktree without explicit opt-in. uv-style:
	// sync is always correct — drift either heals or fails, never persists
	// silently. --allow-drift is the rare escape hatch for local debug.
	// --frozen tolerates drift (restore-from-lock, never fail on staleness).
	if len(driftReports) > 0 && !syncFrozen && (!syncAllowDrift || syncLocked) {
		return fmt.Errorf("sync: %s failed integrity check (pass --allow-drift to downgrade to a warning)", output.Plural(len(driftReports), "entry", "entries"))
	}
	// Reconciler-collected per-entry failures (install, symlink, checkout)
	// are printed individually above via printer.Error. Without this guard,
	// sync's overall exit code stayed 0 even when an install or checkout
	// failed (issue #94). Use errTextHandled so Execute() exits 1 without
	// duplicating the per-entry lines into a trailing `Error: …` envelope —
	// same pattern as batch `qvr add` partial-failure semantics.
	if len(result.Errors) > 0 {
		return errTextHandled
	}
	return nil
}

// verifySyncDrift runs the subtree-hash/commit drift verification over every
// installed (non-link, non-disabled) lock entry, partitioning the results into
// hard drift/failed reports and soft "unverified" (no recorded hash) reports.
func verifySyncDrift(lock *model.LockFile, projectRoot string) (driftReports, unverifiedReports []skill.VerifyEntryResult) {
	for _, entry := range lock.Entries() {
		if entry.IsLink() || entry.Disabled {
			continue
		}
		r := skill.VerifySingleEntry(entry, projectRoot)
		switch r.Status {
		case skill.VerifyStatusDrift, skill.VerifyStatusFailed:
			driftReports = append(driftReports, r)
		case skill.VerifyStatusUnverified:
			unverifiedReports = append(unverifiedReports, r)
		}
	}
	return driftReports, unverifiedReports
}

// emitSyncJSON writes the reconcile result as JSON and applies the same
// exit-code contract as text mode: --check failures, drift (unless --frozen /
// --allow-drift), and per-entry reconcile errors flip the exit non-zero via
// errJSONHandled so the stream stays a single JSON document (#125).
func emitSyncJSON(result *skill.ReconcileResult, driftReports []skill.VerifyEntryResult, checkFailed bool) error {
	if err := printer.JSON(result); err != nil {
		return err
	}
	// JSON mode must honor the same exit-code contract as text mode: drift
	// (issue #65/#118) and per-entry reconcile failures (#94) flip the exit
	// non-zero. Before this, the JSON branch returned right after emitting
	// the payload, so `qvr sync --output json` exited 0 even on drift or a
	// failed restore (#125). errJSONHandled exits 1 without a second
	// envelope so the stream stays one JSON document.
	if checkFailed {
		return errJSONHandled
	}
	// --frozen tolerates drift (it restores from the lock as-is and never
	// fails on staleness); --locked still escalates it.
	if len(driftReports) > 0 && !syncFrozen && (!syncAllowDrift || syncLocked) {
		return errJSONHandled
	}
	if len(result.Errors) > 0 {
		return errJSONHandled
	}
	return nil
}

// renderSyncSummary prints the text-mode reconcile summary: qvr.toml advisories
// first, then restored/linked/removed/skipped/error lines, the block-severity
// roll-up, and finally drift + unverified findings.
func renderSyncSummary(result *skill.ReconcileResult, atOrAboveThreshold map[string]security.Severity, driftReports, unverifiedReports []skill.VerifyEntryResult, projWarnings []string) {
	// qvr.toml reconciliation advisories (ref-conflict "lock wins", install
	// failures for hand-added skills) print before the reconcile summary so the
	// user sees intent-level notes first.
	for _, w := range projWarnings {
		printer.Warning(w)
	}
	for _, name := range result.Installed {
		if sev, ok := atOrAboveThreshold[name]; ok {
			// Tag restored skills that triggered findings ≥ block_severity
			// so a top-down read of the output doesn't end on a clean tick
			// when the just-restored skill has a critical finding.
			printer.Warning(fmt.Sprintf("restored %s — scan found %s findings (see above)", name, sev))
		} else {
			printer.Success(fmt.Sprintf("Restored %s", name))
		}
	}
	for _, path := range result.SymlinksFixed {
		printer.Info(fmt.Sprintf("Linked %s", path))
	}
	for _, path := range result.Removed {
		printer.Warning(fmt.Sprintf("removed orphan %s", path))
	}
	for _, skipped := range result.Skipped {
		printer.Info(fmt.Sprintf("Skipped %s", skipped))
	}
	for _, e := range result.Errors {
		printer.Error(e)
	}
	if len(atOrAboveThreshold) > 0 {
		names := make([]string, 0, len(atOrAboveThreshold))
		for n := range atOrAboveThreshold {
			names = append(names, n)
		}
		sort.Strings(names)
		printer.Warning(fmt.Sprintf("%s raised findings at or above block_severity: %s — review and `qvr remove <name>` or `qvr switch <name> <safer-ref>` if needed",
			output.Plural(len(names), "skill"), strings.Join(names, ", ")))
	}
	// Drift findings — issue #65 (subtreeHash) + issue #73 (commit SHA).
	// Surfaced after the reconcile summary so the user sees what was restored
	// before what looks suspect. Under --strict, any non-OK entry flips the
	// command to a non-zero exit; otherwise these are warnings and sync still
	// returns success.
	for _, d := range driftReports {
		renderDriftReport(d)
	}
	// Un-sealed entries (no recorded hash) warn but never fail — same verdict
	// `qvr lock verify` gives them (#154). The hint points at the now-working
	// `qvr lock upgrade` backfill (#151), not --allow-drift.
	for _, u := range unverifiedReports {
		renderDriftReport(u)
	}
}

// applyProjectFileToLock reconciles qvr.toml (declarative intent) against the
// in-memory lock before the main sync reconcile. It returns advisories to
// surface plus the count of skills qvr.toml declares that the lock lacks (these
// flip `qvr sync --check`, since a real sync would change the lock).
//
//   - Case C (in qvr.toml, missing from lock): resolve + install into the lock
//     (apply mode only). The reconcile that follows materialises it.
//   - Case B (ref differs between qvr.toml and the lock): the LOCK WINS — warn.
//     `qvr lock --from-toml` is the explicit verb to apply qvr.toml's ref.
//   - Case D (portable lock entry absent from qvr.toml): synthesise the entry
//     (apply mode), healing projects that predate qvr.toml.
//
// apply=false (dry-run/check/locked) reports only — no install, no file writes.
// The lock stays self-sufficient: an absent/empty qvr.toml makes this a no-op.
func applyProjectFileToLock(projPath string, lock *model.LockFile, installer *skill.Installer, cfg *config.Config, projectRoot, lockPath string, apply bool) (warnings []string, wouldChange int) {
	existed := true
	if _, err := os.Stat(projPath); errors.Is(err, os.ErrNotExist) {
		existed = false
	}
	proj, err := model.ReadProjectFile(projPath)
	if err != nil {
		return []string{fmt.Sprintf("qvr.toml: %v (ignored; reconciling from qvr.lock only)", err)}, 0
	}

	// Index lock entries by their qvr.toml coordinate for O(1) lookup.
	lockByCoord := make(map[string]*model.LockEntry)
	for _, e := range lock.Entries() {
		if c := model.SkillCoordinate(e); c != "" {
			lockByCoord[c] = e
		}
	}

	// Lazily resolve install targets — only needed if a case-C skill exists.
	var installTargets []string
	var targetsErr error
	resolveTargets := func() ([]string, error) {
		if installTargets == nil && targetsErr == nil {
			installTargets, targetsErr = resolveProjectDefaultTargets(proj, cfg)
		}
		return installTargets, targetsErr
	}

	// Cases B & C: walk qvr.toml's declared skills.
	for _, coord := range proj.SkillCoordinates() {
		ref := proj.SkillRef(coord)
		if entry, ok := lockByCoord[coord]; ok {
			if entry.Ref != ref { // Case B — lock wins.
				warnings = append(warnings, fmt.Sprintf(
					"%s: qvr.toml requests %q but qvr.lock pins %q — keeping the lock; run `qvr lock --from-toml` to apply qvr.toml or `qvr add %s@%s` to update both",
					coord, ref, entry.Ref, coord, ref))
			}
			continue
		}
		// Case C — declared but not locked.
		if !apply {
			wouldChange++
			warnings = append(warnings, fmt.Sprintf("%s@%s is in qvr.toml but not qvr.lock — run `qvr sync` to install it", coord, ref))
			continue
		}
		installed, w := installCaseCSkill(coord, ref, proj, lock, installer, cfg, projectRoot, lockPath, resolveTargets)
		warnings = append(warnings, w...)
		if installed {
			wouldChange++
		}
	}

	// Case D: synthesise qvr.toml entries for portable lock skills it omits.
	// Apply mode only — read-only modes never write the file.
	if apply {
		warnings = synthesizeProjectFileFromLock(proj, lock, projPath, existed, warnings)
	}
	return warnings, wouldChange
}

// installCaseCSkill installs a single case-C skill (declared in qvr.toml but
// absent from the lock) into the lock, resolving targets from the per-skill
// override or the project defaults. Returns whether the install succeeded plus
// any advisory warnings; a malformed coordinate, target error, or install
// failure yields installed=false and a warning rather than an error.
func installCaseCSkill(coord, ref string, proj *model.ProjectFile, lock *model.LockFile, installer *skill.Installer, cfg *config.Config, projectRoot, lockPath string, resolveTargets func() ([]string, error)) (installed bool, warnings []string) {
	reg, name, ok := splitCoordinate(coord)
	if !ok {
		return false, []string{fmt.Sprintf("%s: malformed coordinate in qvr.toml (expected <registry>/<skill>); skipped", coord)}
	}
	// A per-skill target override (qvr.toml inline table) wins over the
	// project default-targets so a `qvr add --target` routing survives a
	// regenerate-from-front-door (#228); a bare entry falls back to defaults.
	var targets []string
	var terr error
	if override := proj.SkillTargets(coord); len(override) > 0 {
		targets, terr = canonicalizeTargets(override)
	} else {
		targets, terr = resolveTargets()
	}
	if terr != nil {
		return false, []string{fmt.Sprintf("%s: cannot install from qvr.toml — %v", coord, terr)}
	}
	if _, ierr := installer.InstallInto(skill.InstallRequest{
		Skill:                    name + "@" + ref,
		Targets:                  targets,
		ProjectRoot:              projectRoot,
		LockPath:                 lockPath,
		Registry:                 reg,
		RequireSigned:            cfg.Security.RequireSigned,
		TrustedAuthors:           trustedAuthorsForRegistry(cfg, reg),
		TrustedAuthorsByRegistry: trustedAuthorsByRegistry(cfg),
	}, lock); ierr != nil {
		return false, []string{fmt.Sprintf("%s@%s: failed to install from qvr.toml (%v) — register its source with `qvr registry add` or run `qvr add`", coord, ref, ierr)}
	}
	printer.Success(fmt.Sprintf("Installed %s@%s (declared in qvr.toml)", coord, ref))
	return true, nil
}

// synthesizeProjectFileFromLock back-fills qvr.toml (case D) with any portable
// lock skill it omits, seeding the [project] block first when creating the file
// fresh, then writing if anything changed. Returns warnings with any write
// failure appended. Apply-mode only — the caller gates on `apply`.
func synthesizeProjectFileFromLock(proj *model.ProjectFile, lock *model.LockFile, projPath string, existed bool, warnings []string) []string {
	changed := false
	// Reconstruct the [project] block first when creating qvr.toml fresh so a
	// lost qvr.toml doesn't silently degrade routing — default-targets is set
	// to the project's dominant routing (mode), and per-skill outliers are
	// recorded inline below. Seeding before the back-fill lets the back-fill
	// decide which skills need an explicit target override.
	if !existed {
		seedSynthesizedProjectMeta(proj, lock, projPath)
		changed = true
	}
	defaults := canonicalizeTargetsQuiet(proj.Project.DefaultTargets)
	for _, e := range lock.Entries() {
		coord := model.SkillCoordinate(e)
		if coord == "" {
			continue
		}
		if !proj.HasSkill(coord) {
			proj.PutSkillSpec(coord, e.Ref, skillTargetOverride(e.Targets, defaults))
			changed = true
		}
	}
	if changed {
		if werr := proj.Write(); werr != nil {
			warnings = append(warnings, fmt.Sprintf("failed to update qvr.toml (%v); qvr.lock is authoritative", werr))
		} else if !existed {
			printer.Hint("created qvr.toml from qvr.lock — commit it so teammates get the declarative config (`git add qvr.toml`)")
		}
	}
	return warnings
}

// resolveProjectDefaultTargets picks install targets for a case-C sync install,
// mirroring add's precedence below the --target flag: qvr.toml defaults, then
// the machine-local config default_target.
func resolveProjectDefaultTargets(proj *model.ProjectFile, cfg *config.Config) ([]string, error) {
	if len(proj.Project.DefaultTargets) > 0 {
		return canonicalizeTargets(proj.Project.DefaultTargets)
	}
	if raw := config.ParseDefaultTargets(cfg.DefaultTarget); len(raw) > 0 {
		return canonicalizeTargets(raw)
	}
	return nil, fmt.Errorf("no [project].default-targets in qvr.toml and config default_target is unset — set one with `qvr target add <name>`")
}

// splitCoordinate splits a qvr.toml skill coordinate "<registry>/<skill>" into
// its registry name (which itself contains a "/", e.g. "org/repo") and the
// trailing skill segment. Returns ok=false for a malformed coordinate.
func splitCoordinate(coord string) (registry, skill string, ok bool) {
	i := strings.LastIndex(coord, "/")
	if i <= 0 || i >= len(coord)-1 {
		return "", "", false
	}
	return coord[:i], coord[i+1:], true
}

// boolCount returns how many of the given flags are true. Used to enforce
// mutual exclusivity of the --locked / --frozen / --check sync modes.
func boolCount(flags ...bool) int {
	n := 0
	for _, f := range flags {
		if f {
			n++
		}
	}
	return n
}

// printOrphanHintIfBig walks the shared worktree cache and emits a
// one-line cleanup hint when orphans cross either the count (5) or size
// (100 MB) threshold. Walk failures are silent — the hint is a courtesy,
// not a contract.
func printOrphanHintIfBig() {
	entries, _, err := collectCacheEntries()
	if err != nil {
		return
	}
	var orphanCount int
	var orphanBytes int64
	for _, e := range entries {
		if !e.Reachable {
			orphanCount++
			orphanBytes += e.SizeBytes
		}
	}
	const (
		orphanCountThreshold = 5
		orphanBytesThreshold = 100 * 1024 * 1024 // 100 MB
	)
	if orphanCount >= orphanCountThreshold || orphanBytes >= orphanBytesThreshold {
		printer.Hint(fmt.Sprintf("%s (~%s) — run `qvr cache prune` to reclaim",
			output.Plural(orphanCount, "orphan worktree"), humanBytes(orphanBytes)))
	}
}

// needsLockWrite reports whether the in-memory lock would serialise to
// bytes different from what's already on disk. Used to make `qvr sync`
// idempotent: a no-state-change rerun should leave qvr.lock byte-identical
// (issue #79). Marshalling-only equivalent of `lock.Write` — no temp
// file, no rename.
//
// Returns true (write) on any marshal failure or empty prior — fail
// open. The only "stable no-op" path is when both serialisations succeed
// and produce the same bytes.
func needsLockWrite(lock *model.LockFile, priorBytes []byte) bool {
	if len(priorBytes) == 0 {
		return true
	}
	// Mirror lockfile.Write exactly: same TOML encoder and trailing newline.
	data, err := model.MarshalLockFile(lock)
	if err != nil {
		return true
	}
	return !bytes.Equal(data, priorBytes)
}

// renderDriftReport prints a single VerifyEntryResult to the package
// printer. VerifySingleEntry packs both subtreeHash and commit drift into
// the same Drift slice (issue #65 + issue #73), so we iterate per-item and
// pick a kind-specific message rather than a one-size template — a
// commit-only tamper used to render as "subtreeHash drift — recorded ,
// on disk <hash>" which hid the commit drift from the user even though
// the verifier caught it.
func renderDriftReport(d skill.VerifyEntryResult) {
	switch d.Status {
	case skill.VerifyStatusDrift:
		for _, item := range d.Drift {
			switch item.Field {
			case "subtreeHash":
				printer.Warning(fmt.Sprintf("%s: subtreeHash drift — recorded %s, on disk %s; run `qvr lock verify --repair` to seal the current content or `qvr remove %s --force && qvr add %s` to restore from upstream",
					d.Name, shortHashLabel(item.Expected), shortHashLabel(item.Actual), d.Name, d.Name))
			case "commit":
				printer.Warning(fmt.Sprintf("%s: commit drift — lockfile %s not reachable from worktree HEAD %s — tampered or detached (issue #73); run `qvr doctor` for the full integrity report",
					d.Name, shortHashLabel(item.Expected), shortHashLabel(item.Actual)))
			default:
				printer.Warning(fmt.Sprintf("%s: %s drift — recorded %s, on disk %s",
					d.Name, item.Field, shortHashLabel(item.Expected), shortHashLabel(item.Actual)))
			}
		}
	case skill.VerifyStatusUnverified:
		printer.Warning(fmt.Sprintf("%s: %s", d.Name, d.Message))
	case skill.VerifyStatusFailed:
		printer.Error(fmt.Sprintf("%s: hash check failed — %s", d.Name, d.Message))
	}
}

// scanRestoredSkillsAfterSync runs the standard scan gate against every
// installed (non-link, non-disabled) entry in lock and surfaces findings.
// Sync is restorative — the lock already committed to these refs — so a
// blocked finding only WARNS rather than rolling back. The user can act on
// the surfaced findings with `qvr remove <name>` or `qvr switch <name> <ref>`
// to a safer version.
//
// Returns a name → highest-severity map for entries whose scan met or exceeded
// the configured block threshold; callers use it to tag the post-render
// summary so success messages for those skills aren't misleading (bug #59).
// autoRegisterRegistriesFromLock looks at every non-link lock entry and,
// for each (registry, source URL) pair where the named registry isn't yet
// in the user config, adds it. This is the v5 follow-on to making the lock
// self-contained: `source` is the authoritative URL, so a fresh clone can
// re-derive the registry pointer set without the user touching config.yaml.
//
// Collision safety: if `cfg.Registries[name]` is already set to a different
// URL we leave it alone and warn — overwriting could silently swap a
// user's pinned registry for whatever the lock author named.
//
// Skips:
//   - link installs (no upstream URL)
//   - entries without a Registry name (ad-hoc URL installs use `source`
//     directly; nothing to register)
//   - entries whose Source is an absolute path (link semantics — would
//     not make a valid git remote)
//
// dryRun=true skips the config.Save call so `qvr sync --dry-run` doesn't
// mutate config.yaml — the dry-run contract is "no filesystem changes."
// The notice on stderr is still emitted (prefixed `would register ...`)
// so the user sees what a real sync would do.
//
// Write failures (real-run) are logged to stderr but do not abort sync —
// the worst case is the user runs `qvr registry add` manually for the
// missing entries.
func autoRegisterRegistriesFromLock(lock *model.LockFile, dryRun bool) {
	if lock == nil {
		return
	}
	cfg, err := config.Load()
	if err != nil {
		return
	}
	if cfg.Registries == nil {
		cfg.Registries = make(map[string]config.RegistryConfig)
	}
	verb := "registered"
	if dryRun {
		verb = "would register"
	}
	var added []string
	var warned bool
	for _, entry := range lock.Entries() {
		if autoRegisterEntry(entry, cfg, verb) {
			added = append(added, entry.Registry)
		}
	}
	if len(added) == 0 {
		_ = warned
		return
	}
	if dryRun {
		// Dry-run already announced each pending registration above;
		// skip the persist step so no filesystem mutation happens.
		return
	}
	if saveErr := config.Save(cfg); saveErr != nil {
		fmt.Fprintf(printer.Err, "%s auto-register: failed to persist config (%v); registries %v added in-memory only\n",
			printer.StyleErr().BoldYellow("warning:"), saveErr, added)
	}
}

// autoRegisterEntry registers a single lock entry's (registry, source) pointer
// into cfg when the named registry isn't already configured, returning true if
// it was added. A registry already configured with a different URL is left
// alone and warned about (collision safety); link/empty/absolute-path entries
// are skipped. In-memory mutation only — the caller persists cfg once.
func autoRegisterEntry(entry *model.LockEntry, cfg *config.Config, verb string) bool {
	if entry == nil || entry.IsLink() {
		return false
	}
	if entry.Registry == "" || entry.Source == "" {
		return false
	}
	// Absolute path in Source means a link install — already
	// excluded by IsLink, but defence-in-depth in case future code
	// paths produce a non-link entry with a local Source. filepath.IsAbs
	// is cross-platform, so Windows drive-letter ("C:\...") and UNC
	// ("\\server\share\...") sources are also treated as local rather
	// than auto-registered as registry URLs.
	if filepath.IsAbs(entry.Source) {
		return false
	}
	existing, ok := cfg.Registries[entry.Registry]
	if !ok {
		cfg.Registries[entry.Registry] = config.RegistryConfig{URL: entry.Source}
		fmt.Fprintf(printer.Err, "%s registry %q → %s (from qvr.lock)\n", verb, entry.Registry, entry.Source)
		return true
	}
	if existing.URL != entry.Source {
		fmt.Fprintf(printer.Err,
			"%s registry %q already configured with a different URL; lock expects %s, config has %s — leaving config unchanged\n",
			printer.StyleErr().BoldYellow("warning:"), entry.Registry, entry.Source, existing.URL)
	}
	return false
}

func scanRestoredSkillsAfterSync(ctx context.Context, lock *model.LockFile, cfg *config.Config, projectRoot string) map[string]security.Severity {
	if !gateAvailable(cfg, syncNoScan) {
		return nil
	}
	flagged := map[string]security.Severity{}
	for _, entry := range lock.Entries() {
		if sev, blocked := scanRestoredEntry(ctx, entry, cfg, projectRoot); blocked {
			flagged[entry.Name] = sev
		}
	}
	return flagged
}

// scanRestoredEntry scans a single restored lock entry, persisting the scan
// signal onto its in-memory verification block. It returns the max severity and
// whether the gate blocked. Skipped entries (links/disabled, no target, or an
// unchanged subtree already attested) and scan failures return blocked=false.
func scanRestoredEntry(ctx context.Context, entry *model.LockEntry, cfg *config.Config, projectRoot string) (security.Severity, bool) {
	if entry.Disabled || entry.IsLink() {
		return "", false
	}
	skillDir := skill.EffectiveTarget(entry, projectRoot)
	if skillDir == "" {
		return "", false
	}
	// Idempotency optimization (issue #79): if we already have a scan
	// attestation for an entry whose on-disk subtree hasn't changed
	// since that scan, skip the rescan entirely. Sync becomes silent
	// on no-state-change reruns. The recorded subtreeHash IS the
	// fingerprint of what was scanned; if the hash still matches what
	// we'd compute now, the prior scan still describes the on-disk
	// state and the per-skill scan output is just noise.
	if entry.Verification != nil && entry.Verification.Scan != nil && entry.SubtreeHash != "" {
		if curHash, herr := skill.ComputeSubtreeHash(skillDir, ""); herr == nil && curHash == entry.SubtreeHash {
			return "", false
		}
	}
	// WarnOnly=true so the `warning:` template is used even for critical
	// findings — sync never blocks, and the `error: … scan blocked` template was
	// misleading the user into thinking the restore was aborted when in
	// fact the symlink was created two lines later (bug #59).
	gate, gerr := ScanAndGate(ctx, skillDir, cfg, scanGateOptions{
		Action:   "sync",
		Subject:  entry.Name,
		WarnOnly: true,
	})
	if gerr != nil || gate == nil || gate.Result == nil {
		return "", false
	}
	// Persist the scan signal onto the entry's verification block.
	// In-memory mutation only — the caller writes the lock once at
	// the end of sync (the WithLock-held window).
	if scan := toScanRef(gate); scan != nil {
		if entry.Verification == nil {
			entry.Verification = &model.VerificationRecord{}
		}
		entry.Verification.Scan = scan
	}
	if gate.Blocked {
		return gate.Result.Summary.MaxSeverity(), true
	}
	return "", false
}

// scanAndPersistRestored rescans every restored skill and persists the scan
// signal onto the in-memory lock, writing qvr.lock only when reconcile + scan
// actually changed the serialised bytes (issue #79 idempotency). It returns the
// name → highest-severity map for entries that met or exceeded block_severity.
func scanAndPersistRestored(ctx context.Context, lock *model.LockFile, cfg *config.Config, projectRoot, lockPath string) (map[string]security.Severity, error) {
	// Snapshot the lock's current on-disk bytes so we can skip the write when
	// reconcile + scan changed nothing (issue #79: sync should be idempotent —
	// no rewrite on no-state-change reruns). Snapshot taken before the scan
	// pass because that's the last in-memory mutation source.
	priorBytes, _ := os.ReadFile(lockPath)
	flagged := scanRestoredSkillsAfterSync(ctx, lock, cfg, projectRoot)
	if needsLockWrite(lock, priorBytes) {
		if werr := lock.Write(); werr != nil {
			return nil, fmt.Errorf("persist scan results: %w", werr)
		}
	}
	return flagged, nil
}
