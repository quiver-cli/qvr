package cmd

import (
	"errors"
	"fmt"
	"os"
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

--frozen makes any drift or missing-worktree status a non-zero exit.
--strict also fails on unverified entries.
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
		"exit non-zero on any drift or missing worktree")
	lockVerifyCmd.Flags().BoolVar(&lockVerifyStrict, "strict", false,
		"also fail on unverified entries (no recorded hash)")
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
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return fmt.Errorf("read lock: %w", err)
	}
	if len(lock.Skills) == 0 {
		printer.Info("No installed skills.")
		if printer.Format == output.FormatJSON {
			return printer.JSON(&VerifyOutput{LockVersion: lock.Version, Entries: []skill.VerifyEntryResult{}})
		}
		return nil
	}

	out := &VerifyOutput{LockVersion: lock.Version, Entries: make([]skill.VerifyEntryResult, 0, len(lock.Skills))}
	changed := false
	for _, entry := range lock.Entries() {
		result := skill.VerifySingleEntry(entry)
		out.Entries = append(out.Entries, result)

		switch result.Status {
		case skill.VerifyStatusOK:
			out.Summary.OK++
		case skill.VerifyStatusDrift:
			out.Summary.Drift++
			if lockVerifyRepair {
				skill.RefreshVerification(entry)
				changed = true
			}
		case skill.VerifyStatusUnverified:
			out.Summary.Unverified++
			if lockVerifyRepair {
				skill.RefreshVerification(entry)
				changed = true
			}
		case skill.VerifyStatusMissing:
			out.Summary.Missing++
		case skill.VerifyStatusLink:
			out.Summary.Link++
		case skill.VerifyStatusFailed:
			out.Summary.Failed++
		}
	}

	if changed {
		if err := lock.Write(); err != nil {
			return fmt.Errorf("write lock after repair: %w", err)
		}
	}

	// Compute the failure string (if any) before emitting JSON so the
	// envelope can carry it as a sibling field. Two top-level documents
	// on stdout would break `jq` / `JSON.parse` and was the v0.4.4 doctor
	// regression pattern repeating itself in the supply-chain commands.
	var failure string
	if lockVerifyFrozen && (out.Summary.Drift > 0 || out.Summary.Missing > 0 || out.Summary.Failed > 0) {
		failure = fmt.Sprintf("lock verify: --frozen failed (%s)", failureCategories(out.Summary))
	} else if lockVerifyStrict && out.Summary.Unverified > 0 {
		failure = fmt.Sprintf("lock verify: --strict failed (%d unverified)", out.Summary.Unverified)
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
	printer.Info(fmt.Sprintf(
		"Summary: %d ok, %d drift, %d unverified, %d missing, %d link, %d failed",
		out.Summary.OK, out.Summary.Drift, out.Summary.Unverified,
		out.Summary.Missing, out.Summary.Link, out.Summary.Failed,
	))
}

// failureCategories renders only the non-zero failing counts so the error
// message names what actually broke. "drift=0, missing=1" reads cleanly;
// the old "drift detected" lied when the real cause was a missing worktree
// or a hash-computation failure.
func failureCategories(s VerifySummary) string {
	pairs := []struct {
		label string
		count int
	}{
		{"drift", s.Drift},
		{"missing", s.Missing},
		{"failed", s.Failed},
	}
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
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return fmt.Errorf("read lock: %w", err)
	}

	// LockVersion reports the version of the file *on disk*, not the
	// migration target — matches `qvr lock verify`, and lets tooling
	// decide "does this repo need a migration?" by reading the field.
	out := &UpgradeOutput{
		LockVersion: lock.Version,
		Entries:     []UpgradeEntryResult{}, // always [], never null
		DryRun:      lockUpgradeDryRun,
	}
	changed := false
	for _, entry := range lock.Entries() {
		row := UpgradeEntryResult{Name: entry.Name}
		switch {
		case entry.Source == "link":
			row.Status = "skipped"
			row.Message = "link install — no provenance to record"
		case entry.Verification != nil && entry.Verification.SubtreeHash != "":
			row.Status = "unchanged"
		default:
			if lockUpgradeDryRun {
				// "would-upgrade" is the dry-run sentinel — distinct from
				// "upgraded" so CI gates like `.entries[] | select(.status == "upgraded")`
				// don't fire on a dry-run pass.
				row.Status = "would-upgrade"
				row.Message = "would compute subtree hash"
			} else {
				skill.RefreshVerification(entry)
				if entry.Verification != nil && entry.Verification.SubtreeHash != "" {
					row.Status = "upgraded"
					changed = true
				} else {
					row.Status = "skipped"
					if entry.Verification != nil && len(entry.Verification.Warnings) > 0 {
						row.Message = entry.Verification.Warnings[0]
					} else {
						row.Message = "could not compute subtree hash"
					}
				}
			}
		}
		out.Entries = append(out.Entries, row)
	}

	if changed && !lockUpgradeDryRun {
		if err := lock.Write(); err != nil {
			return fmt.Errorf("write lock: %w", err)
		}
		// After a successful write the on-disk version is now LockFileVersion.
		// Reflect that in the output so JSON consumers don't see a stale
		// pre-migration value next to "upgraded" rows.
		out.LockVersion = model.LockFileVersion
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
