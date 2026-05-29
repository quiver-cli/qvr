package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/registry"
	"github.com/raks097/quiver/internal/security"
	"github.com/raks097/quiver/internal/skill"
	"github.com/spf13/cobra"
)

var (
	syncGlobal        bool
	syncDryRun        bool
	syncKeepUntracked bool
	syncNoScan        bool
	syncStrict        bool
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
	syncCmd.Flags().BoolVar(&syncStrict, "strict", false,
		"fail (non-zero exit) when any restored worktree's content hash diverges from the lock's recorded subtreeHash; default is to warn and continue")
	rootCmd.AddCommand(syncCmd)
}

func runSync(cmd *cobra.Command, args []string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), syncGlobal)

	var (
		result             *skill.ReconcileResult
		latestLock         *model.LockFile
		atOrAboveThreshold map[string]security.Severity
		driftReports       []skill.VerifyEntryResult
	)
	lockErr := model.WithLock(lockPath, func() error {
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
		autoRegisterRegistriesFromLock(lock, syncDryRun)

		gc := git.NewGoGitClient()
		wt := git.NewGoGitWorktree()
		installer := skill.NewInstaller(newRegistryManager(gc), wt, gc)
		reconciler := skill.NewReconciler(installer)

		r, err := reconciler.Reconcile(lock, projectRoot, config.Dir(), skill.ReconcileOptions{
			DryRun:        syncDryRun,
			KeepUntracked: syncKeepUntracked,
		})
		if err != nil {
			return fmt.Errorf("sync: %w", err)
		}
		result = r
		latestLock = lock

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
			cfg, cerr := config.Load()
			if cerr == nil {
				atOrAboveThreshold = scanRestoredSkillsAfterSync(cmd.Context(), lock, cfg)
				if werr := lock.Write(); werr != nil {
					return fmt.Errorf("persist scan results: %w", werr)
				}
			}

			// Subtree-hash drift check (issue #65). The reconciler restored
			// every worktree from the lock's recorded commit, but it does
			// not verify the on-disk content matches the lock's recorded
			// SubtreeHash. A stale, corrupted, or tampered entry passes
			// reconcile cleanly and only surfaces on the next
			// `qvr lock verify`. Run that verification here so sync is the
			// integrity gate the user expects: warn on drift by default,
			// promote to a non-zero exit under --strict.
			for _, entry := range lock.Entries() {
				if entry.IsLink() || entry.Disabled {
					continue
				}
				r := skill.VerifySingleEntry(entry)
				switch r.Status {
				case skill.VerifyStatusDrift, skill.VerifyStatusFailed, skill.VerifyStatusUnverified:
					driftReports = append(driftReports, r)
				}
			}
		}
		return nil
	})
	if lockErr != nil {
		return lockErr
	}

	registry.TouchProject(lockPath)

	// Refresh AGENTS.md if the user has opted in (file already present). The
	// reconciler may have changed which skills are visible, so the doc cache
	// can otherwise lie until the next manual `qvr docs`.
	if !syncGlobal && !syncDryRun && latestLock != nil {
		_ = refreshAgentsMDIfPresent(projectRoot, latestLock.Entries())
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(result)
	}

	for _, name := range result.Installed {
		if sev, ok := atOrAboveThreshold[name]; ok {
			// Tag restored skills that triggered findings ≥ block_severity
			// so a top-down read of the output doesn't end on a clean tick
			// when the just-restored skill has a critical finding.
			printer.Warning(fmt.Sprintf("Restored %s — scan found %s findings (see above)", name, sev))
		} else {
			printer.Success(fmt.Sprintf("Restored %s", name))
		}
	}
	for _, path := range result.SymlinksFixed {
		printer.Info(fmt.Sprintf("Linked %s", path))
	}
	for _, path := range result.Removed {
		printer.Warning(fmt.Sprintf("Removed orphan %s", path))
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
		printer.Warning(fmt.Sprintf("%d skill(s) raised findings at or above block_severity: %s — review and `qvr remove <name>` or `qvr switch <name> <safer-ref>` if needed",
			len(names), strings.Join(names, ", ")))
	}
	// Drift findings — issue #65. Surfaced after the reconcile summary so
	// the user sees what was restored before what looks suspect. Under
	// --strict, any non-OK entry flips the command to a non-zero exit;
	// otherwise these are warnings and sync still returns success.
	for _, d := range driftReports {
		switch d.Status {
		case skill.VerifyStatusDrift:
			printer.Warning(fmt.Sprintf("%s: subtreeHash drift — recorded %s, on disk %s; run `qvr lock verify --repair` to seal the current content or `qvr remove %s --force && qvr add %s` to restore from upstream",
				d.Name, shortHashLabel(driftExpected(d)), shortHashLabel(d.SubtreeHash), d.Name, d.Name))
		case skill.VerifyStatusUnverified:
			printer.Warning(fmt.Sprintf("%s: %s", d.Name, d.Message))
		case skill.VerifyStatusFailed:
			printer.Error(fmt.Sprintf("%s: hash check failed — %s", d.Name, d.Message))
		}
	}

	if len(result.Installed)+len(result.SymlinksFixed)+len(result.Removed) == 0 && len(result.Errors) == 0 && len(atOrAboveThreshold) == 0 && len(driftReports) == 0 {
		printer.Success("Already in sync.")
	}
	if syncStrict && len(driftReports) > 0 {
		return fmt.Errorf("sync --strict: %d entr(y/ies) failed integrity check", len(driftReports))
	}
	return nil
}

// driftExpected returns the recorded SubtreeHash that the drift entry
// disagreed with. Pulled from the structured Drift slice the verifier
// emits so the render shows the *expected* hash alongside the on-disk one.
func driftExpected(r skill.VerifyEntryResult) string {
	for _, d := range r.Drift {
		if d.Field == "subtreeHash" {
			return d.Expected
		}
	}
	return ""
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
		if entry == nil || entry.IsLink() {
			continue
		}
		if entry.Registry == "" || entry.Source == "" {
			continue
		}
		// Absolute path in Source means a link install — already
		// excluded by IsLink, but defence-in-depth in case future code
		// paths produce a non-link entry with a local Source.
		if strings.HasPrefix(entry.Source, "/") {
			continue
		}
		existing, ok := cfg.Registries[entry.Registry]
		if !ok {
			cfg.Registries[entry.Registry] = config.RegistryConfig{URL: entry.Source}
			added = append(added, entry.Registry)
			fmt.Fprintf(printer.Err, "%s registry %q → %s (from qvr.lock)\n", verb, entry.Registry, entry.Source)
			continue
		}
		if existing.URL != entry.Source {
			fmt.Fprintf(printer.Err,
				"registry %q already configured with a different URL; lock expects %s, config has %s — leaving config unchanged\n",
				entry.Registry, entry.Source, existing.URL)
			warned = true
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
		fmt.Fprintf(printer.Err, "auto-register: failed to persist config (%v); registries %v added in-memory only\n", saveErr, added)
	}
}

func scanRestoredSkillsAfterSync(ctx context.Context, lock *model.LockFile, cfg *config.Config) map[string]security.Severity {
	if !gateAvailable(cfg, syncNoScan) {
		return nil
	}
	flagged := map[string]security.Severity{}
	for _, entry := range lock.Entries() {
		if entry.Disabled || entry.IsLink() {
			continue
		}
		skillDir := skill.EffectiveTarget(entry)
		if skillDir == "" {
			continue
		}
		// WarnOnly=true so the ⚠ template is used even for critical findings
		// — sync never blocks, and the old "✗ scan blocked" template was
		// misleading the user into thinking the restore was aborted when in
		// fact the symlink was created two lines later (bug #59).
		gate, gerr := ScanAndGate(ctx, skillDir, cfg, scanGateOptions{
			Action:   "sync",
			Subject:  entry.Name,
			WarnOnly: true,
		})
		if gerr != nil || gate == nil || gate.Result == nil {
			continue
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
			flagged[entry.Name] = gate.Result.Summary.MaxSeverity()
		}
	}
	return flagged
}
