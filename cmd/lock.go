package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/registry"
	"github.com/raks097/quiver/internal/skill"
	"github.com/spf13/cobra"
)

var lockCmd = &cobra.Command{
	Use:   "lock",
	Short: "Re-resolve qvr.lock (and inspect/maintain it via subcommands)",
	Long: `With no subcommand, re-resolve the lock: for every registry-backed entry,
fetch its source and re-pin the recorded commit to the current tip of the
pinned ref, rewriting qvr.lock WITHOUT touching installs (no worktrees are
checked out, no symlinks change). This is the standalone re-resolve verb
(mirrors ` + "`uv lock`" + `); run ` + "`qvr sync`" + ` afterwards to materialise the
re-pinned commits.

Skills have no transitive dependencies yet, so "re-resolve" means "re-pin each
ref to its latest commit," not dependency solving. Re-pinned entries have their
content hash invalidated and are restored + re-hashed by the next ` + "`qvr sync`" + `.

  -P, --package <name>   re-pin only this skill
      --dry-run          report what would change without writing
      --global           operate on the user-global lock

Subcommands re-hash installed skills and detect drift from the recorded
supply-chain provenance (` + "`verify`" + `), or backfill missing provenance
(` + "`upgrade`" + `).`,
	RunE: runLock,
}

var (
	lockVerifyFrozen bool
	lockVerifyStrict bool
	lockVerifyRepair bool
	lockVerifyGlobal bool
	lockVerifyFailOn string

	lockUpgradeDryRun bool
	lockUpgradeGlobal bool

	lockResolvePackage string
	lockResolveDryRun  bool
	lockResolveGlobal  bool
)

var lockVerifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Re-hash installed skills and report drift from the lock file",
	Long: `Walk every entry in qvr.lock, recompute the canonical subtree hash
from the on-disk worktree, and compare against the recorded value. Reports
per-skill status: ok, drift, unverified (no hash on file), missing (worktree
gone), link (no upstream), or failed (hash computation errored).

Exit code reflects detected drift so CI can gate on it (issue #156): by
default any drift, missing-worktree, or failed-hash entry exits non-zero.
Tune which states are fatal with --fail-on:
  --fail-on drift       (default) drift / missing / failed are fatal
  --fail-on unverified  also fail on unverified entries (no recorded hash)
  --fail-on none        always exit 0 (report only — the old behavior)

--frozen is a shorthand for --fail-on drift, --strict for --fail-on
unverified; both are kept for compatibility and win over a weaker --fail-on.
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
	lockVerifyCmd.Flags().StringVar(&lockVerifyFailOn, "fail-on", "drift",
		"which states exit non-zero: none | drift (drift/missing/failed) | unverified (+ no recorded hash)")
	lockVerifyCmd.Flags().BoolVar(&lockVerifyFrozen, "frozen", false,
		"shorthand for --fail-on drift (kept for compatibility)")
	lockVerifyCmd.Flags().BoolVar(&lockVerifyStrict, "strict", false,
		"shorthand for --fail-on unverified (kept for compatibility)")
	lockVerifyCmd.Flags().BoolVar(&lockVerifyRepair, "repair", false,
		"rewrite drifting Verification blocks using current worktree state")
	lockVerifyCmd.Flags().BoolVar(&lockVerifyGlobal, "global", false,
		"operate on the user-global lock file instead of the project lock")

	lockUpgradeCmd.Flags().BoolVar(&lockUpgradeDryRun, "dry-run", false,
		"report changes without writing")
	lockUpgradeCmd.Flags().BoolVar(&lockUpgradeGlobal, "global", false,
		"operate on the user-global lock file instead of the project lock")

	lockCmd.Flags().StringVarP(&lockResolvePackage, "package", "P", "",
		"re-pin only this skill (like uv lock -P pkg)")
	lockCmd.Flags().BoolVar(&lockResolveDryRun, "dry-run", false,
		"report which entries would be re-pinned without writing the lock")
	lockCmd.Flags().BoolVar(&lockResolveGlobal, "global", false,
		"operate on the user-global lock file instead of the project lock")

	lockCmd.AddCommand(lockVerifyCmd, lockUpgradeCmd)
	rootCmd.AddCommand(lockCmd)
}

// LockResolveEntryResult is one row of `qvr lock` (standalone re-resolve).
type LockResolveEntryResult struct {
	Name string `json:"name"`
	Ref  string `json:"ref,omitempty"`
	// Status vocabulary mirrors `qvr lock upgrade`'s verbs:
	//   "repinned"    — wrote a new commit to the entry
	//   "would-repin" — --dry-run says we'd re-pin
	//   "unchanged"   — ref already at its tip commit
	//   "skipped"     — link/edit/standalone entry with no registry upstream
	//   "failed"      — couldn't resolve the ref (e.g. registry not fetched)
	Status    string `json:"status"`
	OldCommit string `json:"oldCommit,omitempty"`
	NewCommit string `json:"newCommit,omitempty"`
	Message   string `json:"message,omitempty"`
}

// LockResolveOutput is the top-level shape `qvr lock` emits in JSON mode.
type LockResolveOutput struct {
	LockVersion int                      `json:"lockVersion"`
	Entries     []LockResolveEntryResult `json:"entries"`
	DryRun      bool                     `json:"dryRun"`
}

func runLock(cmd *cobra.Command, args []string) error {
	// A positional arg here is a mistyped subcommand (e.g. `qvr lock verfiy`):
	// preserve the #120 "unknown command" non-zero exit rather than silently
	// re-resolving. Real re-resolve takes no positionals — `-P` is a flag.
	if len(args) > 0 {
		return fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath())
	}
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), lockResolveGlobal)

	var out *LockResolveOutput
	lockErr := model.WithLock(lockPath, func() error {
		o, err := lockResolveInternal(cmd.Context(), lockPath)
		if err != nil {
			return err
		}
		out = o
		return nil
	})
	if lockErr != nil {
		return lockErr
	}

	failed := 0
	repinned := 0
	for _, e := range out.Entries {
		switch e.Status {
		case "failed":
			failed++
		case "repinned":
			repinned++
		}
	}

	if printer.Format == output.FormatJSON {
		if err := printer.JSON(out); err != nil {
			return err
		}
		if failed > 0 {
			// Body carries the per-entry failure; suppress the duplicate
			// top-level envelope so stdout stays a single JSON document.
			return errJSONHandled
		}
		return nil
	}

	if len(out.Entries) == 0 {
		printer.Info("No installed skills.")
		return nil
	}
	for _, e := range out.Entries {
		switch e.Status {
		case "repinned":
			printer.Success(fmt.Sprintf("%s: re-pinned %s %s → %s", e.Name, e.Ref, e.OldCommit, e.NewCommit))
		case "would-repin":
			printer.Info(fmt.Sprintf("%s: would re-pin %s %s → %s", e.Name, e.Ref, e.OldCommit, e.NewCommit))
		case "unchanged":
			printer.Info(fmt.Sprintf("%s: unchanged (%s @ %s)", e.Name, e.Ref, e.OldCommit))
		case "skipped":
			printer.Info(fmt.Sprintf("%s: skipped — %s", e.Name, e.Message))
		case "failed":
			printer.Error(fmt.Sprintf("%s: failed — %s", e.Name, e.Message))
		}
	}
	if repinned > 0 && !lockResolveDryRun {
		printer.Info("Run `qvr sync` to materialise the re-pinned commits.")
	}
	if failed > 0 {
		// Per-entry errors already printed above; errTextHandled exits 1
		// without duplicating them into a trailing `Error: …` envelope.
		return errTextHandled
	}
	return nil
}

// lockResolveInternal is the read-modify-write loop for `qvr lock`, extracted
// so it runs inside WithLock. It fetches each referenced registry once, then
// re-pins every (or just --package) registry-backed entry's commit to the
// current tip of its ref. Worktrees and symlinks are left untouched — the
// re-pin only rewrites lock metadata and invalidates the content hash, which
// the next `qvr sync` restores and recomputes.
func lockResolveInternal(ctx context.Context, lockPath string) (*LockResolveOutput, error) {
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return nil, fmt.Errorf("read lock: %w", err)
	}

	// Register missing registries from the lock so RegistryPath/Update can find
	// their bare clones (self-healing, same as `qvr sync`). Dry-run never
	// persists config.
	autoRegisterRegistriesFromLock(lock, lockResolveDryRun)

	gc := git.NewGoGitClient()
	mgr := newRegistryManager(gc)

	out := &LockResolveOutput{
		LockVersion: lock.Version,
		Entries:     []LockResolveEntryResult{}, // always [], never null
		DryRun:      lockResolveDryRun,
	}

	entries := lock.Entries()
	if lockResolvePackage != "" {
		e, err := lock.Get(lockResolvePackage)
		if err != nil {
			return nil, err
		}
		entries = []*model.LockEntry{e}
	}

	// Fetch each distinct registry once. Fetching is cache warming, not a
	// project mutation, so it runs even under --dry-run to give an accurate
	// preview of upstream re-pins. Network failure is non-fatal: resolve
	// against the cached clone (offline-friendly, same posture as `qvr upgrade`).
	fetched := map[string]bool{}
	changed := false
	for _, entry := range entries {
		row := LockResolveEntryResult{Name: entry.Name, Ref: entry.Ref}
		switch {
		case entry.IsLink():
			row.Status = "skipped"
			row.Message = "link install — no upstream to re-resolve"
		case entry.IsEdit():
			row.Status = "skipped"
			row.Message = "edit install — re-pin via `qvr publish`/`qvr edit`, not `qvr lock`"
		case entry.Registry == "":
			row.Status = "skipped"
			row.Message = "no registry upstream to re-resolve"
		default:
			if !fetched[entry.Registry] {
				if _, uerr := mgr.Update(ctx, entry.Registry); uerr != nil {
					printer.Warning(fmt.Sprintf("lock: refresh %s failed (%v); resolving against cached clone", entry.Registry, uerr))
				}
				fetched[entry.Registry] = true
			}
			newCommit, rerr := gc.ResolveRef(registry.RegistryPath(entry.Registry), entry.Ref)
			if rerr != nil {
				row.Status = "failed"
				row.Message = rerr.Error()
				break
			}
			row.OldCommit = registry.ShortSHA(entry.Commit)
			row.NewCommit = registry.ShortSHA(newCommit)
			if newCommit == entry.Commit {
				row.Status = "unchanged"
				break
			}
			if lockResolveDryRun {
				row.Status = "would-repin"
				break
			}
			// Re-pin: rewrite the commit + cache key and recompute the content
			// hash straight from the bare clone's git objects — no worktree
			// checkout, no symlink change. The digest is identical to what a
			// checkout of newCommit would produce, so the lock stays verifiable
			// immediately and the next `qvr sync` materialises a worktree that
			// matches. If hashing fails (unreadable subtree), invalidate the
			// fields so sync recomputes them on restore rather than leaving a
			// stale hash.
			entry.Commit = newCommit
			entry.InstallCommit = registry.ShortSHA(newCommit)
			if id, herr := skill.ComputeEntryIdentityAtCommit(registry.RegistryPath(entry.Registry), newCommit, entry.Path, entry.RootCoexists); herr == nil {
				entry.SubtreeHash = id.SubtreeHash
				entry.TreeOID = id.TreeSHA
			} else {
				entry.SubtreeHash = ""
				entry.TreeOID = ""
			}
			changed = true
			row.Status = "repinned"
		}
		out.Entries = append(out.Entries, row)
	}

	if changed && !lockResolveDryRun {
		if err := lock.Write(); err != nil {
			return nil, fmt.Errorf("write lock: %w", err)
		}
		out.LockVersion = model.LockFileVersion
	}
	return out, nil
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
	failure, ferr := lockVerifyFailure(out.Summary)
	if ferr != nil {
		return nil, "", ferr
	}
	return out, failure, nil
}

// lockVerifyFailLevel ranks the --fail-on vocabulary so --frozen/--strict can
// raise (never lower) the effective threshold. Higher = stricter.
//
//	none(0)        — report only, always exit 0
//	drift(1)       — drift / missing / failed are fatal (the CI-gate default)
//	unverified(2)  — also fail on entries with no recorded hash
func lockVerifyFailLevel(name string) (int, bool) {
	switch name {
	case "none":
		return 0, true
	case "drift":
		return 1, true
	case "unverified":
		return 2, true
	default:
		return 0, false
	}
}

// lockVerifyFailure decides whether the run is a non-zero exit (issue #156:
// verify must gate CI on drift, not silently exit 0). The effective level is
// --fail-on, raised by the legacy --frozen (≥ drift) and --strict
// (= unverified) shorthands so neither can be weakened by a stale default.
func lockVerifyFailure(s VerifySummary) (string, error) {
	level, ok := lockVerifyFailLevel(lockVerifyFailOn)
	if !ok {
		return "", fmt.Errorf("lock verify: invalid --fail-on %q (want none|drift|unverified)", lockVerifyFailOn)
	}
	if lockVerifyFrozen && level < 1 {
		level = 1
	}
	if lockVerifyStrict {
		level = 2
	}
	switch {
	case level >= 2 && (s.Drift > 0 || s.Missing > 0 || s.Failed > 0 || s.Unverified > 0):
		return fmt.Sprintf("lock verify failed (%s)", strictFailureCategories(s)), nil
	case level >= 1 && (s.Drift > 0 || s.Missing > 0 || s.Failed > 0):
		return fmt.Sprintf("lock verify failed (%s)", failureCategories(s)), nil
	}
	return "", nil
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
				id, err := skill.ComputeEntryIdentity(worktreePath, entry.Path, entry.RootCoexists)
				if err != nil || id == nil || id.SubtreeHash == "" {
					row.Status = "skipped"
					if err != nil {
						row.Message = err.Error()
					} else {
						row.Message = "could not compute subtree hash"
					}
				} else {
					entry.SubtreeHash = id.SubtreeHash
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
