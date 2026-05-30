package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/skill"
	"github.com/spf13/cobra"
)

var lockCmd = &cobra.Command{
	Use:   "lock",
	Short: "Inspect and maintain qvr.lock",
	Long: `Manage the on-disk lock file. Subcommands re-hash installed skills and
detect drift from the recorded supply-chain provenance, or migrate older
lockfiles to the current schema.`,
	// Without a RunE, cobra silently exits 0 on `qvr lock <typo>` after
	// printing help — a typo like `lock verfiy` looks like success in CI.
	// Mirror the top-level "unknown command" shape so the exit code matches.
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath())
		}
		return cmd.Help()
	},
}

var (
	lockVerifyFrozen bool
	lockVerifyStrict bool
	lockVerifyRepair bool
	lockVerifyGlobal bool

	lockUpgradeDryRun bool
	lockUpgradeGlobal bool
)

var lockVerifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Re-hash installed skills and report drift from the lock file",
	Long: `Walk every entry in qvr.lock, recompute the canonical subtree hash
from the on-disk worktree, and compare against the recorded value. Reports
per-skill status: ok, drift, unverified (no hash on file), missing (worktree
gone), link (no upstream), or failed (hash computation errored).

--frozen makes any drift, missing-worktree, or failed-hash status a
non-zero exit.
--strict implies --frozen and also fails on unverified entries.
--repair rewrites Verification blocks for drifting entries using current
disk state (use only when you trust the current worktree).`,
	RunE: runLockVerify,
}

var lockUpgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Populate Verification blocks for any entries missing them",
	Long: `Read qvr.lock, compute the canonical subtree hash for every entry
that lacks a Verification block, and write the result back. Safe to re-run
— idempotent for entries that already have a hash.

--dry-run reports which entries would change without writing.`,
	RunE: runLockUpgrade,
}

func init() {
	lockVerifyCmd.Flags().BoolVar(&lockVerifyFrozen, "frozen", false,
		"exit non-zero on any drift, missing-worktree, or failed-hash entry")
	lockVerifyCmd.Flags().BoolVar(&lockVerifyStrict, "strict", false,
		"imply --frozen and also fail on unverified entries (no recorded hash)")
	lockVerifyCmd.Flags().BoolVar(&lockVerifyRepair, "repair", false,
		"rewrite drifting Verification blocks using current worktree state")
	lockVerifyCmd.Flags().BoolVar(&lockVerifyGlobal, "global", false,
		"operate on the user-global lock file instead of the project lock")

	lockUpgradeCmd.Flags().BoolVar(&lockUpgradeDryRun, "dry-run", false,
		"report changes without writing")
	lockUpgradeCmd.Flags().BoolVar(&lockUpgradeGlobal, "global", false,
		"operate on the user-global lock file instead of the project lock")

	lockCmd.AddCommand(lockVerifyCmd, lockUpgradeCmd)
	rootCmd.AddCommand(lockCmd)
}

// VerifySummary aggregates per-status counts for the JSON output.
type VerifySummary struct {
	OK         int `json:"ok"`
	Drift      int `json:"drift"`
	Unverified int `json:"unverified"`
	Missing    int `json:"missing"`
	Link       int `json:"link"`
	Failed     int `json:"failed"`
	Repaired   int `json:"repaired,omitempty"`
}

// VerifyOutput is the top-level shape `qvr lock verify` emits in JSON mode.
type VerifyOutput struct {
	LockVersion int                       `json:"lockVersion"`
	Entries     []skill.VerifyEntryResult `json:"entries"`
	Summary     VerifySummary             `json:"summary"`
	// Error populates only on --frozen / --strict failure paths and lets
	// JSON consumers parse stdout as a single document. The text path uses
	// the same string as the printed `Error: ...` line on stderr.
	Error string `json:"error,omitempty"`
}

func runLockVerify(cmd *cobra.Command, args []string) error {
	_ = args
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), lockVerifyGlobal)

	var (
		out     *VerifyOutput
		empty   bool
		failure string
	)
	lockErr := model.WithLock(lockPath, func() error {
		lock, err := model.ReadLockFile(lockPath)
		if err != nil {
			return fmt.Errorf("read lock: %w", err)
		}
		if len(lock.Skills) == 0 {
			empty = true
			out = &VerifyOutput{LockVersion: lock.Version, Entries: []skill.VerifyEntryResult{}}
			return nil
		}
		o, fail, err := lockVerifyInternal(lock, projectRoot)
		if err != nil {
			return err
		}
		out = o
		failure = fail
		return nil
	})
	if lockErr != nil {
		return lockErr
	}
	if empty {
		printer.Info("No installed skills.")
		if printer.Format == output.FormatJSON {
			return printer.JSON(out)
		}
		return nil
	}

	if printer.Format == output.FormatJSON {
		if failure != "" {
			out.Error = failure
		}
		if err := printer.JSON(out); err != nil {
			return err
		}
		if failure != "" {
			// errJSONHandled suppresses Execute()'s {"error": "..."} envelope —
			// the body's `error` field already carries the failure string, so
			// stdout stays a single JSON document.
			return errJSONHandled
		}
		return nil
	}

	renderVerifyText(out)
	if failure != "" {
		return errors.New(failure)
	}
	return nil
}

// lockVerifyInternal is the read-modify-write loop extracted out of
// runLockVerify so it can run inside WithLock. Returns the result, an
// optional --frozen/--strict failure string, and any fatal error.
func lockVerifyInternal(lock *model.LockFile, projectRoot string) (*VerifyOutput, string, error) {
	out := &VerifyOutput{LockVersion: lock.Version, Entries: make([]skill.VerifyEntryResult, 0, len(lock.Skills))}
	changed := false
	for _, entry := range lock.Entries() {
		result := skill.VerifySingleEntry(entry, projectRoot)

		switch result.Status {
		case skill.VerifyStatusOK:
			out.Summary.OK++
		case skill.VerifyStatusDrift:
			if lockVerifyRepair {
				repair := skill.RepairSubtreeHashFromDisk(entry, projectRoot)
				if repair.Failed {
					// Couldn't compute a fresh hash — leave the drift
					// report intact so the user still sees what diverged.
					result.Message = "repair failed: " + repair.Error
					out.Summary.Drift++
				} else {
					result.Status = skill.VerifyStatusRepaired
					result.SubtreeHash = repair.NewSubtreeHash
					result.OldSubtreeHash = repair.OldSubtreeHash
					result.Drift = nil
					out.Summary.Repaired++
					changed = true
				}
			} else {
				out.Summary.Drift++
			}
		case skill.VerifyStatusUnverified:
			if lockVerifyRepair {
				repair := skill.RepairSubtreeHashFromDisk(entry, projectRoot)
				if repair.Failed {
					result.Message = "repair failed: " + repair.Error
					out.Summary.Unverified++
				} else {
					result.Status = skill.VerifyStatusRepaired
					result.SubtreeHash = repair.NewSubtreeHash
					result.OldSubtreeHash = repair.OldSubtreeHash
					out.Summary.Repaired++
					changed = true
				}
			} else {
				out.Summary.Unverified++
			}
		case skill.VerifyStatusMissing:
			out.Summary.Missing++
		case skill.VerifyStatusLink:
			out.Summary.Link++
		case skill.VerifyStatusFailed:
			out.Summary.Failed++
		}

		out.Entries = append(out.Entries, result)
	}

	if changed {
		if err := lock.Write(); err != nil {
			return nil, "", fmt.Errorf("write lock after repair: %w", err)
		}
	}

	// Compute the failure string (if any) before emitting JSON so the
	// envelope can carry it as a sibling field. Two top-level documents
	// on stdout would break `jq` / `JSON.parse` and was the v0.4.4 doctor
	// regression pattern repeating itself in the supply-chain commands.
	// --strict implies --frozen plus unverified: a CI gate that runs
	// `--strict` is asking for "every entry is verifiably the recorded
	// state." Without folding the --frozen failure modes into --strict, a
	// missing worktree would slip through with exit 0 — the regression that
	// motivated this rule (audit, OSS-readiness pass).
	var failure string
	switch {
	case lockVerifyStrict && (out.Summary.Drift > 0 || out.Summary.Missing > 0 || out.Summary.Failed > 0 || out.Summary.Unverified > 0):
		failure = fmt.Sprintf("lock verify: --strict failed (%s)", strictFailureCategories(out.Summary))
	case lockVerifyFrozen && (out.Summary.Drift > 0 || out.Summary.Missing > 0 || out.Summary.Failed > 0):
		failure = fmt.Sprintf("lock verify: --frozen failed (%s)", failureCategories(out.Summary))
	}
	return out, failure, nil
}

// failureCategories renders only the non-zero failing counts so the error
// message names what actually broke. "drift=0, missing=1" reads cleanly;
// the old "drift detected" lied when the real cause was a missing worktree
// or a hash-computation failure.
func failureCategories(s VerifySummary) string {
	return renderFailureCategories([]failingCategory{
		{"drift", s.Drift},
		{"missing", s.Missing},
		{"failed", s.Failed},
	})
}

// strictFailureCategories is failureCategories plus the unverified bucket,
// since --strict additionally fails on entries lacking a recorded hash.
func strictFailureCategories(s VerifySummary) string {
	return renderFailureCategories([]failingCategory{
		{"drift", s.Drift},
		{"missing", s.Missing},
		{"failed", s.Failed},
		{"unverified", s.Unverified},
	})
}

type failingCategory struct {
	label string
	count int
}

func renderFailureCategories(pairs []failingCategory) string {
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		if p.count > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", p.label, p.count))
		}
	}
	if len(parts) == 0 {
		return "no failing entries"
	}
	return strings.Join(parts, ", ")
}

func renderVerifyText(out *VerifyOutput) {
	for _, r := range out.Entries {
		switch r.Status {
		case skill.VerifyStatusOK:
			printer.Success(fmt.Sprintf("%s: ok", r.Name))
		case skill.VerifyStatusDrift:
			printer.Warning(fmt.Sprintf("%s: drift (%d field(s))", r.Name, len(r.Drift)))
			for _, d := range r.Drift {
				printer.Warning(fmt.Sprintf("  %s: expected %s, got %s", d.Field, shortHashLabel(d.Expected), shortHashLabel(d.Actual)))
			}
		case skill.VerifyStatusRepaired:
			if r.OldSubtreeHash != "" {
				printer.Success(fmt.Sprintf("%s: repaired (subtreeHash %s → %s)",
					r.Name, shortHashLabel(r.OldSubtreeHash), shortHashLabel(r.SubtreeHash)))
			} else {
				printer.Success(fmt.Sprintf("%s: repaired (subtreeHash %s)",
					r.Name, shortHashLabel(r.SubtreeHash)))
			}
		case skill.VerifyStatusUnverified:
			printer.Warning(fmt.Sprintf("%s: unverified — %s", r.Name, r.Message))
		case skill.VerifyStatusMissing:
			printer.Error(fmt.Sprintf("%s: missing — %s", r.Name, r.Message))
		case skill.VerifyStatusLink:
			printer.Info(fmt.Sprintf("%s: link (skipped)", r.Name))
		case skill.VerifyStatusFailed:
			printer.Error(fmt.Sprintf("%s: failed — %s", r.Name, r.Message))
		}
	}
	parts := []string{
		fmt.Sprintf("%d ok", out.Summary.OK),
		fmt.Sprintf("%d drift", out.Summary.Drift),
		fmt.Sprintf("%d unverified", out.Summary.Unverified),
		fmt.Sprintf("%d missing", out.Summary.Missing),
		fmt.Sprintf("%d link", out.Summary.Link),
		fmt.Sprintf("%d failed", out.Summary.Failed),
	}
	if out.Summary.Repaired > 0 {
		parts = append(parts, fmt.Sprintf("%d repaired", out.Summary.Repaired))
	}
	printer.Info("Summary: " + strings.Join(parts, ", "))
}

// shortHashLabel renders a hash for terminal output without losing the
// algorithm prefix when present. "sha256:abcd..." → "sha256:abcd1234"
// rather than "abcd1234".
func shortHashLabel(h string) string {
	if h == "" {
		return "(none)"
	}
	if len(h) <= 14 {
		return h
	}
	return h[:14] + "..."
}

// UpgradeEntryResult is one row of `qvr lock upgrade` output.
type UpgradeEntryResult struct {
	Name string `json:"name"`
	// Status vocabulary matches the text-mode verbs:
	//   "upgraded"      — wrote a new subtree hash to disk
	//   "would-upgrade" — --dry-run says we'd write
	//   "unchanged"     — entry already had a hash + complete provenance
	//   "skipped"       — link install, or hash computation failed
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// UpgradeOutput is the top-level shape `qvr lock upgrade` emits in JSON mode.
type UpgradeOutput struct {
	LockVersion int                  `json:"lockVersion"`
	Entries     []UpgradeEntryResult `json:"entries"`
	DryRun      bool                 `json:"dryRun"`
}

func runLockUpgrade(cmd *cobra.Command, args []string) error {
	_ = args
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), lockUpgradeGlobal)

	var out *UpgradeOutput
	lockErr := model.WithLock(lockPath, func() error {
		o, err := lockUpgradeInternal(lockPath)
		if err != nil {
			return err
		}
		out = o
		return nil
	})
	if lockErr != nil {
		return lockErr
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(out)
	}
	for _, e := range out.Entries {
		switch e.Status {
		case "would-upgrade":
			printer.Info(fmt.Sprintf("%s: would upgrade", e.Name))
		case "upgraded":
			printer.Success(fmt.Sprintf("%s: upgraded", e.Name))
		case "unchanged":
			printer.Info(fmt.Sprintf("%s: unchanged", e.Name))
		case "skipped":
			printer.Warning(fmt.Sprintf("%s: skipped — %s", e.Name, e.Message))
		}
	}
	return nil
}

func lockUpgradeInternal(lockPath string) (*UpgradeOutput, error) {
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return nil, fmt.Errorf("read lock: %w", err)
	}
	// LockVersion reports the version of the file *on disk*. v5 reaches this
	// path having already passed ReadLockFile's version gate. The job here is
	// twofold:
	//   1. Fill any entry with a missing top-level SubtreeHash (e.g. installs
	//      where the initial hash computation failed, or a hand-edited lock).
	//   2. Re-run the configured scan gate against entries that lack a
	//      verification.scan block, persisting the structured ScanRef so the
	//      help text's "populate[s] Verification blocks for any entries
	//      missing them" promise holds (issue #63). Skipped when
	//      security.scan_on_install isn't configured — upgrade then only
	//      fills the hash and the status reads "upgraded (hash only)".
	cfg, _ := config.Load()
	out := &UpgradeOutput{
		LockVersion: lock.Version,
		Entries:     []UpgradeEntryResult{}, // always [], never null
		DryRun:      lockUpgradeDryRun,
	}
	changed := false
	for _, entry := range lock.Entries() {
		row := UpgradeEntryResult{Name: entry.Name}
		switch {
		case entry.IsLink():
			row.Status = "skipped"
			row.Message = "link install — no upstream subtree to hash"
		case entry.SubtreeHash == "":
			if lockUpgradeDryRun {
				row.Status = "would-upgrade"
				row.Message = "would compute subtree hash"
			} else {
				worktreePath := skill.EntryWorktreePath(entry)
				hash, err := skill.ComputeSubtreeHash(worktreePath, entry.Path)
				if err != nil || hash == "" {
					row.Status = "skipped"
					if err != nil {
						row.Message = err.Error()
					} else {
						row.Message = "could not compute subtree hash"
					}
				} else {
					entry.SubtreeHash = hash
					row.Status = "upgraded"
					changed = true
				}
			}
		default:
			row.Status = "unchanged"
		}

		// Issue #63 — also restore the verification.scan block when the
		// gate is configured and the entry is missing one. Runs on both
		// freshly-hashed entries (status="upgraded") and previously
		// unchanged entries (status="unchanged") that just lack the
		// scan record. We only mutate row.Status when we actually wrote
		// the scan, so dry-run / skipped / link rows pass through.
		if !lockUpgradeDryRun && !entry.IsLink() && entry.SubtreeHash != "" &&
			(entry.Verification == nil || entry.Verification.Scan == nil) &&
			gateAvailable(cfg, false) {
			worktreePath := skill.EntryWorktreePath(entry)
			skillDir := worktreePath
			if entry.Path != "" {
				skillDir = filepath.Join(worktreePath, entry.Path)
			}
			gate, gerr := ScanAndGate(context.Background(), skillDir, cfg, scanGateOptions{
				Action:   "lock upgrade",
				Subject:  entry.Name,
				WarnOnly: true,
			})
			if gerr == nil && gate != nil && !gate.Skipped {
				if scan := toScanRef(gate); scan != nil {
					if entry.Verification == nil {
						entry.Verification = &model.VerificationRecord{}
					}
					entry.Verification.Scan = scan
					changed = true
					// Promote unchanged rows to "upgraded" so callers
					// see that something happened. Hash-side upgrades
					// stay "upgraded".
					if row.Status == "unchanged" {
						row.Status = "upgraded"
						row.Message = "restored verification.scan"
					}
				}
			}
		}

		out.Entries = append(out.Entries, row)
	}

	if changed && !lockUpgradeDryRun {
		if err := lock.Write(); err != nil {
			return nil, fmt.Errorf("write lock: %w", err)
		}
		out.LockVersion = model.LockFileVersion
	}
	return out, nil
}
