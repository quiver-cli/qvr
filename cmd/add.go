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
	"github.com/astra-sh/qvr/internal/skill"
	"github.com/spf13/cobra"
)

var (
	addTargets  []string
	addGlobal   bool
	addForce    bool
	addFrozen   bool
	addNoScan   bool
	addRegistry string
	addAs       string
	addAll      bool
	addLocal    string
)

var addCmd = &cobra.Command{
	Use:   "add <skill>[@<ref>]...",
	Short: "Add one or more skills from registered sources to the project lock",
	Long: `Add a skill (by name) from any registered source to the current project's
lock file. The skill is resolved against every configured registry; pin a
specific branch, tag, or commit with @<ref>.

  qvr add tdd                       # writes ./qvr.lock, symlinks .claude/skills/tdd
  qvr add tdd@v2                    # pin a branch or tag
  qvr add --global diagnose         # ambient lane: appears in every Claude session
  qvr add tdd lint review           # batch add — each must resolve to a registered skill

One-step install — no prior 'qvr registry add' needed. Point at a skill inside a
repo and qvr auto-registers the source, then installs the skill:

  qvr add github.com/org/repo/tdd        # register org/repo, install tdd
  qvr add github.com/org/repo/tdd@v2     # …pinned to a ref
  qvr add github.com/org/repo            # single-skill repo: installs the lone skill

Or register a source explicitly first, then add by name:

  qvr registry add <url>

Bulk and local modes skip naming a skill:

  qvr add --all --registry acme          # install every skill the registry exposes
  qvr add --local ./my-skill             # install an immutable copy of a local folder

The lockfile is the only source of truth for what the agent loads. Anything
under .claude/skills/ that isn't in qvr.lock is hidden on the next ` + "`qvr sync`" + `.`,
	// --all and --local install without naming a skill, so positional args are
	// only required (and only accepted) in the default by-name mode. Cobra
	// parses flags before validating args, so the flag vars are already set.
	// Custom messages here beat cobra's generic `unknown command "<arg>"`
	// (which reads as a typo'd subcommand, not "this mode takes no skill name").
	Args: func(cmd *cobra.Command, args []string) error {
		if addAll {
			if len(args) > 0 {
				return fmt.Errorf("--all installs every skill in the registry — don't also name a skill (got %q)", strings.Join(args, " "))
			}
			return nil
		}
		if addLocal != "" {
			if len(args) > 0 {
				return fmt.Errorf("--local takes the folder path as its flag value — don't also name a skill (got %q)", strings.Join(args, " "))
			}
			return nil
		}
		return cobra.MinimumNArgs(1)(cmd, args)
	},
	RunE: runAdd,
}

func init() {
	addCmd.Flags().StringSliceVar(&addTargets, "target", nil,
		"agent target(s) to install into (repeatable). Defaults to default_target (which may itself be comma-separated, e.g. \"claude,cursor\").")
	addCmd.Flags().BoolVar(&addGlobal, "global", false,
		"write to the user-global lock and symlink under ~/.<agent>/skills/ instead of the project")
	addCmd.Flags().BoolVar(&addForce, "force", false,
		"allow replacing an existing lock entry at a different ref")
	addCmd.Flags().BoolVar(&addFrozen, "frozen", false,
		"refuse drift from the recorded subtree hash; the skill must already be in the lock")
	addCmd.Flags().BoolVar(&addNoScan, "no-scan", false,
		"skip the security scan that normally gates installs (override security.scan_on_install)")
	addCmd.Flags().StringVar(&addRegistry, "registry", "",
		"scope resolution to a single registry (defaults to all configured); use to disambiguate same-named skills")
	addCmd.Flags().StringVar(&addAs, "as", "",
		"install under a different local name (lock entry + symlink filename). Lets two versions of the same skill coexist in one project for A/B testing. Single skill only.")
	addCmd.Flags().BoolVar(&addAll, "all", false,
		"install every skill the registry exposes (requires --registry; do not name a skill)")
	addCmd.Flags().StringVar(&addLocal, "local", "",
		"install an immutable copy of a skill from a local folder (no registry; `qvr edit` to make it mutable)")
	rootCmd.AddCommand(addCmd)
}

func runAdd(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := enforceScanPolicy(cfg, addNoScan); err != nil {
		return err
	}
	// --all / --local are alternate install modes that skip naming a skill.
	// Validate their mutual exclusions first so e.g. `add --all --as foo`
	// reports "--as cannot be combined with --all" rather than the generic
	// single-skill `--as` complaint below (which would say "got 0").
	if err := validateAddModes(); err != nil {
		return err
	}
	// --as renames a single lock entry; with multiple positional args it
	// would silently apply to only one and skip the rest, which would be
	// the kind of "looks like it worked" footgun the rest of `qvr add`
	// guards against. Refuse rather than guess.
	if addAs != "" && len(args) != 1 {
		return fmt.Errorf("--as can only be used with a single skill argument (got %d)", len(args))
	}
	if err := validateAddAsEmpty(cmd); err != nil {
		return err
	}

	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), addGlobal)
	projPath := model.DefaultProjectPath(projectRoot)

	targets, err := resolveAddTargets(cfg, projectRoot)
	if err != nil {
		return err
	}

	// Opportunistically warm registry caches for the next command (#211).
	// Best-effort and OFF by default; it spawns a detached refresh and returns
	// immediately, never blocking or failing this add. Deferred so it fires
	// after the install work on every add mode (default / --all / --local).
	defer registry.MaybePrefetch()

	gc := git.NewGoGitClient()
	wt := git.NewGoGitWorktree()
	mgr := newRegistryManager(gc)
	installer := skill.NewInstaller(mgr, wt, gc)

	// --local installs an immutable copy of a local folder — a distinct path
	// (copy + scan + lock) that never touches registry resolution.
	if addLocal != "" {
		return runAddLocal(cmd, cfg, installer, projectRoot, lockPath, targets)
	}

	// One-step install: an arg shaped like a clone URL with a skill path
	// (e.g. github.com/org/repo/skill) auto-registers its registry here, before
	// the lock window, then flows through as a plain name scoped to that source.
	// Plain skill names pass straight through with the global --registry scope.
	// --all enumerates every skill the named registry exposes instead.
	var items []addItem
	if addAll {
		items, err = resolveAllItems(mgr, cfg)
		if err != nil {
			return err
		}
	} else {
		var perr error
		items, perr = resolveAddItems(cmd.Context(), mgr, args)
		if perr != nil {
			return perr
		}
	}

	prematerializeAddItems(installer, cfg, items, targets, projectRoot, lockPath)

	var st addInstallState
	lockErr := model.WithLock(config.Dir(), lockPath, func() error {
		return runAddInstallLoop(cmd, cfg, installer, items, targets, projectRoot, lockPath, projPath, &st)
	})
	if lockErr != nil {
		return lockErr
	}

	return emitAddResults(st.results, st.blocked, st.firstErr, projectRoot, lockPath)
}

// validateAddAsEmpty rejects an explicit empty `--as ""`, which would otherwise
// reach the installer indistinguishable from "flag not passed" and silently
// install under the canonical name (issue #103). It routes the rejection through
// the same printer/envelope path the rest of add uses (issue #121).
func validateAddAsEmpty(cmd *cobra.Command) error {
	if !cmd.Flags().Changed("as") || addAs != "" {
		return nil
	}
	err := fmt.Errorf("invalid --as value %q: must be 1-64 chars, lowercase alphanumeric + hyphens, no leading/trailing or consecutive hyphens", addAs)
	// Issue #121: route through the same printer/envelope path the
	// rest of add uses. Pre-fix `--as ""` returned the bare error,
	// so text mode rendered `Error: …` (Execute's default envelope)
	// while every other add failure rendered `✗ add …: …`. JSON mode
	// emitted `{"error": "..."}` here vs the legacy
	// `{"installed": [], "error": "..."}` elsewhere — two distinct
	// shapes from the same command.
	if printer.Format == output.FormatJSON {
		payload := buildAddJSONEnvelope(nil, nil, err)
		if jerr := printer.JSON(payload); jerr != nil {
			return jerr
		}
		return errJSONHandled
	}
	printer.Error(fmt.Sprintf("add: %v", err))
	return errTextHandled
}

// prematerializeAddItems warms every skill's content dir concurrently before the
// lock window (#206) when more than one item is being installed — the expensive,
// independent part of an install. The serial Install loop then reuses each
// pre-built dir; it's a pure optimization, so the request gating must mirror the
// serial loop (RequireSigned/Force) for the pre-pass to resolve identically.
func prematerializeAddItems(installer *skill.Installer, cfg *config.Config, items []addItem, targets []string, projectRoot, lockPath string) {
	if len(items) <= 1 {
		return
	}
	batch := make([]skill.InstallRequest, 0, len(items))
	for _, item := range items {
		batch = append(batch, skill.InstallRequest{
			Skill:       item.skillRef,
			Targets:     targets,
			Global:      addGlobal,
			ProjectRoot: projectRoot,
			LockPath:    lockPath,
			Force:       addForce,
			Frozen:      addFrozen,
			Registry:    item.registry,
			SkillPath:   item.skillPath,
			As:          addAs,
			// Mirror the serial loop's gating so the pre-pass resolves and
			// warms identically — in particular RequireSigned drives the
			// fresh-provenance decision (a require_signed install must NOT be
			// served a cached signature), and Force matches the conflict-check
			// behavior so a --force re-add resolves the same in both passes.
			RequireSigned:            cfg.Security.RequireSigned,
			TrustedAuthors:           trustedAuthorsForRegistry(cfg, item.registry),
			TrustedAuthorsByRegistry: trustedAuthorsByRegistry(cfg),
		})
	}
	installer.PrematerializeBatch(batch)
}

// addInstallState accumulates the batch install outcome across the WithLock
// window for runAdd to emit afterwards.
type addInstallState struct {
	results  []*skill.InstallResult
	blocked  []blockedSkillJSON
	firstErr error
}

// runAddInstallLoop is the under-lock batch-install body of `qvr add`: read the
// project lock once, install + scan-gate each item (rolling back blocked ones),
// then persist the lock and project file once. Threading a single in-memory lock
// through the whole batch keeps it O(N) rather than O(N²) (#206). Outcomes land
// on st for the caller to render.
func runAddInstallLoop(cmd *cobra.Command, cfg *config.Config, installer *skill.Installer, items []addItem, targets []string, projectRoot, lockPath, projPath string, st *addInstallState) error {
	// Read the project lock ONCE for the whole batch and thread it through
	// every install/remove/scan-record below, writing it a single time after
	// the loop. The serial path used to ReadLockFile + Write the full lock per
	// skill (and again per scan record) — O(N²) lockfile churn that dominated
	// the warm multi-skill install. The whole loop runs single-threaded inside
	// this WithLock window, so the in-memory lock is the only writer.
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return fmt.Errorf("read lock file: %w", err)
	}
	for _, item := range items {
		addInstallItem(cmd, cfg, installer, item, targets, projectRoot, lockPath, lock, st)
	}
	// Persist the whole batch's lock mutations once. Partial successes are
	// kept (matching the prior per-skill-write behavior); blocked installs
	// already removed their entry from the in-memory lock above, so they're
	// simply absent from this write.
	if err := lock.Write(); err != nil {
		return fmt.Errorf("write lock file: %w", err)
	}
	// Write-through to qvr.toml (declarative intent). The lock is the
	// authoritative resolved state, so a qvr.toml failure warns rather than
	// failing the add. Global installs have no project file. A single
	// read+write outside the per-skill loop keeps the batch O(N).
	if !addGlobal {
		if perr := syncProjectFileFromLock(projPath, lock, st.results); perr != nil {
			printer.Warning(fmt.Sprintf("recorded in qvr.lock but failed to update qvr.toml (%v); run `qvr sync` to reconcile", perr))
		}
	}
	return nil
}

// addInstallItem installs one batch item into the shared in-memory lock, runs
// the scan gate (rolling back a blocked install atomically), records the scan
// result, and prints the per-skill ✓/✗ output in order (#66). Outcomes (result,
// blocked, firstErr) accumulate on st.
func addInstallItem(cmd *cobra.Command, cfg *config.Config, installer *skill.Installer, item addItem, targets []string, projectRoot, lockPath string, lock *model.LockFile, st *addInstallState) {
	ref := item.skillRef
	result, err := installer.InstallInto(skill.InstallRequest{
		Skill:                    ref,
		Targets:                  targets,
		Global:                   addGlobal,
		ProjectRoot:              projectRoot,
		LockPath:                 lockPath,
		Force:                    addForce,
		Frozen:                   addFrozen,
		Registry:                 item.registry,
		SkillPath:                item.skillPath,
		As:                       addAs,
		RequireSigned:            cfg.Security.RequireSigned,
		TrustedAuthors:           trustedAuthorsForRegistry(cfg, item.registry),
		TrustedAuthorsByRegistry: trustedAuthorsByRegistry(cfg),
	}, lock)
	if err != nil {
		// Skill not found is the headline error — point at `qvr registry add`
		// so the user knows the next step. Everything else falls through with
		// the wrapped error.
		if errors.Is(err, skill.ErrSkillNotFound) {
			err = fmt.Errorf("no registered source contains a skill named %q — register one with `qvr registry add <url>`", ref)
		}
		printer.Error(fmt.Sprintf("add %s: %v", ref, err))
		if st.firstErr == nil {
			st.firstErr = err
		}
		return
	}

	// Security gate. Scan the freshly-installed worktree and roll back
	// the install if findings meet or exceed the configured threshold.
	// Done inside the WithLock window so a blocked install also
	// reverts the lock entry atomically.
	gate, gerr := ScanAndGate(cmd.Context(), skillDirForEntry(result, lock), cfg, scanGateOptions{
		Disabled: addNoScan,
		Action:   "add",
		Subject:  result.Name,
		// Quiet: collapse benign-finding noise to a one-line banner.
		// Blocked installs still get the full detail.
		Quiet: true,
	})
	if gerr != nil {
		printer.Warning(fmt.Sprintf("add %s: scan failed (%v); install kept — rerun `qvr scan %s` to retry", result.Name, gerr, result.Name))
		st.results = append(st.results, result)
		return
	}
	if gate.Blocked {
		removeErr := installer.RemoveFrom(result.Name, skill.InstallRequest{
			ProjectRoot: projectRoot,
			Global:      addGlobal,
			LockPath:    lockPath,
		}, lock)
		if removeErr != nil {
			printer.Error(fmt.Sprintf("add %s: scan blocked, rollback also failed (%v); run `qvr remove %s --force` to clean up", result.Name, removeErr, result.Name))
		}
		blockErr := &blockedScanError{Subject: result.Name, Threshold: gate.Threshold, Result: gate.Result}
		st.blocked = append(st.blocked, blockedSkillFromErr(blockErr))
		if st.firstErr == nil {
			st.firstErr = blockErr
		}
		return
	}
	// Persist the (allowed) scan result onto the lock entry so
	// downstream tools can inspect it without re-running the scan.
	// A write failure here is non-fatal — the install itself
	// succeeded and the user can re-record via `qvr scan`.
	if recErr := recordScanResultInLock(lock, result.Name, gate); recErr != nil {
		printer.Warning(fmt.Sprintf("add %s: scan recorded only in memory (%v)", result.Name, recErr))
	}
	st.results = append(st.results, result)
	// Issue #66: print the success marker inside the loop so
	// per-skill output (scan warnings, then ✓ Added) reads in
	// order. Previously every ✓ printed in a trailing loop
	// after all failures, making partial-failure batches look
	// like total failures on a CI scroll-by.
	if printer.Format != output.FormatJSON {
		// Surface installer-side advisories (e.g. multi-registry
		// ambiguity pick) before the ✓ so the user sees the
		// caveat associated with the install it qualifies
		// (issue #101).
		for _, w := range result.Warnings {
			printer.Warning(w)
		}
		printer.Success(fmt.Sprintf("Added %s@%s → %s", result.Name, result.Version, strings.Join(result.Targets, ", ")))
	}
}

// setProjectFileSkillRef upserts a coordinate→ref into qvr.toml, writing only
// when the ref changed. Used by `qvr switch <ref>` write-through. It will not
// CREATE qvr.toml — adoption happens via add/sync (which synthesise the full
// set); a switch only updates a file that already exists. Non-fatal on error.
func setProjectFileSkillRef(projPath, coord, ref string) error {
	if coord == "" {
		return nil
	}
	if _, err := os.Stat(projPath); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	proj, err := model.ReadProjectFile(projPath)
	if err != nil {
		return err
	}
	if cur, ok := proj.Skill(coord); ok && cur.Ref == ref {
		return nil // already correct — quiet diff
	}
	proj.PutSkill(coord, ref)
	return proj.Write()
}

// dropProjectFileSkills removes the given coordinates from qvr.toml, writing
// only when something changed. Used by `qvr remove` / `qvr edit` write-through
// so a removed (or ejected-to-edit) skill stops being declared — otherwise the
// sync case-C pass would re-install it. A failure warns; the lock is
// authoritative, so it is never fatal.
func dropProjectFileSkills(projPath string, coords []string) error {
	if len(coords) == 0 {
		return nil
	}
	proj, err := model.ReadProjectFile(projPath)
	if err != nil {
		return err
	}
	prior, err := model.MarshalProjectFile(proj)
	if err != nil {
		return err
	}
	for _, c := range coords {
		proj.RemoveSkill(c)
	}
	next, err := model.MarshalProjectFile(proj)
	if err != nil {
		return err
	}
	if bytes.Equal(prior, next) {
		return nil
	}
	return proj.Write()
}

// modeLockTargets reconstructs the project's routing policy
// (`[project].default-targets`) from the lock when qvr.toml has been lost. It
// returns the MODE — the single most common target set across installed skills —
// not the union: skill-A→[claude] + skill-B→[cursor] must rebuild a default of
// the dominant set, with the outlier carried as a per-skill override, NOT the
// widened union [claude,cursor] that would silently re-route every future bare
// `qvr add` into both (#230). Ties break deterministically on the joined set so
// the result is stable. Edit/link/local entries (no portable coordinate) are
// excluded — they aren't representable in qvr.toml and so don't vote on routing.
func modeLockTargets(lock *model.LockFile) []string {
	counts := map[string]int{}    // key -> occurrences
	sets := map[string][]string{} // key -> the canonical target set
	for _, e := range lock.Entries() {
		if model.SkillCoordinate(e) == "" {
			continue
		}
		set := normalizeTargetSet(e.Targets)
		if len(set) == 0 {
			continue
		}
		key := strings.Join(set, "\x00")
		counts[key]++
		sets[key] = set
	}
	bestKey, bestCount := "", 0
	for key, c := range counts {
		if c > bestCount || (c == bestCount && key < bestKey) {
			bestKey, bestCount = key, c
		}
	}
	if bestKey == "" {
		return nil
	}
	return sets[bestKey]
}

// normalizeTargetSet returns a sorted, de-duplicated copy of a target set with
// empties dropped — the canonical form used to compare two target sets.
func normalizeTargetSet(targets []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// sameTargetSet reports whether two target sets are equal as sets.
func sameTargetSet(a, b []string) bool {
	na, nb := normalizeTargetSet(a), normalizeTargetSet(b)
	if len(na) != len(nb) {
		return false
	}
	for i := range na {
		if na[i] != nb[i] {
			return false
		}
	}
	return true
}

// skillTargetOverride returns the per-skill target set to record in qvr.toml for
// a lock entry: nil when the entry's targets match the project default-targets
// (so it stays a bare-string entry), or the entry's targets when they differ (so
// the inline-table form pins the override and a front-door regenerate keeps the
// routing — #228).
func skillTargetOverride(entryTargets, defaultTargets []string) []string {
	if sameTargetSet(entryTargets, defaultTargets) {
		return nil
	}
	return normalizeTargetSet(entryTargets)
}

// canonicalizeTargetsQuiet canonicalizes target names best-effort, dropping any
// that don't resolve. Used where targets are already canonical (lock entries,
// qvr.toml default-targets) and a hard error would be inappropriate.
func canonicalizeTargetsQuiet(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if c, ok := model.CanonicalTarget(strings.TrimSpace(n)); ok {
			out = append(out, c)
		}
	}
	return out
}

// seedSynthesizedProjectMeta fills the [project] block of a qvr.toml being
// CREATED fresh from the lock (a lost or never-adopted qvr.toml). Routing policy
// is the load-bearing part: default-targets is reconstructed from the MODE of
// every installed skill's targets (modeLockTargets) so a dropped qvr.toml doesn't
// silently degrade `qvr add` routing to the machine-local default — while
// outliers are recorded as per-skill overrides by the caller rather than widening
// the default (#230). name/version aren't recorded in the lock, so they get
// init-style defaults rather than an empty, misleading [project] block. Only ever
// called when synthesising a new file, so it never overwrites a user's hand-set
// metadata.
func seedSynthesizedProjectMeta(proj *model.ProjectFile, lock *model.LockFile, projPath string) {
	if len(proj.Project.DefaultTargets) == 0 {
		if mode := modeLockTargets(lock); len(mode) > 0 {
			proj.Project.DefaultTargets = mode
		}
	}
	if proj.Project.Name == "" {
		proj.Project.Name = filepath.Base(filepath.Dir(projPath))
	}
	if proj.Project.Version == "" {
		proj.Project.Version = "0.1.0"
	}
}

// syncProjectFileFromLock updates qvr.toml to reflect the lock after an install.
// It sets the ref for every just-installed portable skill (authoritative), then
// back-fills any other portable lock entry qvr.toml doesn't yet list — the
// auto-synthesis that heals projects predating qvr.toml. Non-portable installs
// (edit/link/local, ad-hoc URLs) have no coordinate and stay lock-only.
//
// Idempotent: writes only when the serialized bytes change, so a no-op re-add
// leaves qvr.toml untouched (quiet diff). The lock stays self-sufficient — this
// only ever projects already-resolved lock state, never the reverse.
func syncProjectFileFromLock(projPath string, lock *model.LockFile, installed []*skill.InstallResult) error {
	existed := true
	if _, err := os.Stat(projPath); errors.Is(err, os.ErrNotExist) {
		existed = false
	}
	proj, err := model.ReadProjectFile(projPath)
	if err != nil {
		return err
	}
	prior, err := model.MarshalProjectFile(proj)
	if err != nil {
		return err
	}
	// Reconstruct the [project] block first when creating qvr.toml fresh, so
	// routing policy (default-targets) survives a lost qvr.toml instead of
	// degrading — and so the per-skill override decision below has a default to
	// compare each entry's targets against.
	if !existed {
		seedSynthesizedProjectMeta(proj, lock, projPath)
	}
	defaults := canonicalizeTargetsQuiet(proj.Project.DefaultTargets)
	// Authoritative ref + per-skill targets for everything installed in this
	// batch. Targets matching default-targets stay bare; an override (e.g. a
	// `qvr add --target`) is pinned inline so it survives a regenerate (#228).
	for _, r := range installed {
		entry, gerr := lock.Get(r.Name)
		if gerr != nil {
			continue
		}
		if coord := model.SkillCoordinate(entry); coord != "" {
			proj.PutSkillSpec(coord, entry.Ref, skillTargetOverride(entry.Targets, defaults))
		}
	}
	// Back-fill: synthesize entries for portable lock skills qvr.toml omits.
	for _, entry := range lock.Entries() {
		coord := model.SkillCoordinate(entry)
		if coord == "" {
			continue
		}
		if !proj.HasSkill(coord) {
			proj.PutSkillSpec(coord, entry.Ref, skillTargetOverride(entry.Targets, defaults))
		}
	}
	next, err := model.MarshalProjectFile(proj)
	if err != nil {
		return err
	}
	if bytes.Equal(prior, next) {
		return nil // quiet diff — nothing to write
	}
	if err := proj.Write(); err != nil {
		return err
	}
	if !existed {
		printer.Hint("created qvr.toml — commit it so teammates get the declarative skill set (`git add qvr.toml`)")
	}
	return nil
}

// resolveAddTargets picks the agent targets a `qvr add` installs into, applying
// the strict, mutually-exclusive precedence:
//
//  1. an explicit --target flag (overrides everything)
//  2. the project's qvr.toml [project].default-targets (set by `qvr target add`)
//  3. the machine-local config default_target
//
// Names are normalised to canonical target names (aliases resolved) so the
// lockfile records a stable, portable identity. qvr.toml defaults are already
// canonical (the target command normalises on write), but are re-validated
// defensively in case the file was hand-edited. Global installs have no project
// file, so they skip the qvr.toml tier straight to config.
func resolveAddTargets(cfg *config.Config, projectRoot string) ([]string, error) {
	if len(addTargets) > 0 {
		return canonicalizeTargets(addTargets)
	}
	// Project routing policy travels in qvr.toml — read it before falling back
	// to machine-local config so teammates reproduce the same routing.
	if !addGlobal {
		if proj, err := model.ReadProjectFile(model.DefaultProjectPath(projectRoot)); err == nil && len(proj.Project.DefaultTargets) > 0 {
			return canonicalizeTargets(proj.Project.DefaultTargets)
		}
	}
	if raw := config.ParseDefaultTargets(cfg.DefaultTarget); len(raw) > 0 {
		return canonicalizeTargets(raw)
	}
	return nil, fmt.Errorf("no --target specified, no project default targets (set with `qvr target add`), and config default_target is unset")
}

// validateAddModes enforces the mutual exclusions between the default by-name
// install and the --all / --local modes. Positional-arg presence is already
// handled by the command's Args validator.
func validateAddModes() error {
	if addAll && addLocal != "" {
		return fmt.Errorf("--all and --local are mutually exclusive")
	}
	if addAll {
		if addRegistry == "" {
			return fmt.Errorf("--all requires --registry <name> to scope which registry to install from")
		}
		if addAs != "" {
			return fmt.Errorf("--as cannot be combined with --all (it would alias every skill to one name)")
		}
	}
	if addLocal != "" {
		if addAs != "" {
			return fmt.Errorf("--as cannot be combined with --local")
		}
		if addRegistry != "" {
			return fmt.Errorf("--registry cannot be combined with --local (a local folder has no registry)")
		}
		if addFrozen {
			return fmt.Errorf("--frozen cannot be combined with --local")
		}
	}
	return nil
}

// resolveAllItems enumerates every skill the --registry registry exposes and
// turns each into an install item scoped to that registry. The registry must
// already be configured. Names are sorted so output and the prematerialize
// batch are deterministic.
func resolveAllItems(mgr *registry.Manager, cfg *config.Config) ([]addItem, error) {
	if _, ok := cfg.Registries[addRegistry]; !ok {
		return nil, fmt.Errorf("registry %q is not configured — add it with `qvr registry add <url>`", addRegistry)
	}
	skills, _, err := mgr.Index(addRegistry, registry.RegistryPath(addRegistry))
	if err != nil {
		return nil, fmt.Errorf("index registry %q: %w", addRegistry, err)
	}
	if len(skills) == 0 {
		return nil, fmt.Errorf("registry %q exposes no installable skills", addRegistry)
	}
	names := make([]string, 0, len(skills))
	for _, s := range skills {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	items := make([]addItem, 0, len(names))
	for _, n := range names {
		items = append(items, addItem{skillRef: n, registry: addRegistry})
	}
	return items, nil
}

// runAddLocal installs an immutable copy of a skill from a local folder
// (`qvr add --local <path>`). It mirrors the registry add's scan gate and
// result envelope so output and JSON shape are identical, but materializes by
// copying the folder rather than resolving a registry. `qvr edit` later ejects
// it to a mutable working copy if needed.
func runAddLocal(cmd *cobra.Command, cfg *config.Config, installer *skill.Installer, projectRoot, lockPath string, targets []string) error {
	resolved, discovered, err := resolveSkillDir(addLocal)
	if err != nil {
		if len(discovered) > 1 {
			printer.Error(err.Error())
			for _, d := range discovered {
				printer.Detail(d)
			}
			return fmt.Errorf("ambiguous skill path")
		}
		return fmt.Errorf("add --local: %w", err)
	}
	if resolved != addLocal {
		printer.Info(fmt.Sprintf("Discovered skill at %s", resolved))
	}

	var result *skill.InstallResult
	var blocked []blockedSkillJSON
	var firstErr error
	lockErr := model.WithLock(config.Dir(), lockPath, func() error {
		r, ierr := installer.InstallLocal(resolved, skill.InstallRequest{
			Targets:     targets,
			Global:      addGlobal,
			ProjectRoot: projectRoot,
			LockPath:    lockPath,
			Force:       addForce,
		})
		if ierr != nil {
			printer.Error(fmt.Sprintf("add --local %s: %v", resolved, ierr))
			firstErr = ierr
			return nil
		}
		// Security gate — same as a registry add (the user opted into scanning
		// local skills). A blocked install is rolled back inside the lock window.
		gate, gerr := ScanAndGate(cmd.Context(), skillDirFor(r, lockPath), cfg, scanGateOptions{
			Disabled: addNoScan,
			Action:   "add",
			Subject:  r.Name,
			Quiet:    true,
		})
		if gerr != nil {
			printer.Warning(fmt.Sprintf("add --local %s: scan failed (%v); install kept — rerun `qvr scan %s` to retry", r.Name, gerr, r.Name))
			result = r
			return nil
		}
		if gate.Blocked {
			if removeErr := installer.Remove(r.Name, skill.InstallRequest{
				ProjectRoot: projectRoot,
				Global:      addGlobal,
				LockPath:    lockPath,
			}); removeErr != nil {
				printer.Error(fmt.Sprintf("add --local %s: scan blocked, rollback also failed (%v); run `qvr remove %s --force` to clean up", r.Name, removeErr, r.Name))
			}
			blockErr := &blockedScanError{Subject: r.Name, Threshold: gate.Threshold, Result: gate.Result}
			blocked = append(blocked, blockedSkillFromErr(blockErr))
			firstErr = blockErr
			return nil
		}
		if recErr := recordScanResult(lockPath, r.Name, gate); recErr != nil {
			printer.Warning(fmt.Sprintf("add --local %s: scan recorded only in memory (%v)", r.Name, recErr))
		}
		result = r
		if printer.Format != output.FormatJSON {
			printer.Success(fmt.Sprintf("Added %s@%s → %s", r.Name, r.Version, strings.Join(r.Targets, ", ")))
		}
		return nil
	})
	if lockErr != nil {
		return lockErr
	}

	var results []*skill.InstallResult
	if result != nil {
		results = append(results, result)
	}
	return emitAddResults(results, blocked, firstErr, projectRoot, lockPath)
}

// emitAddResults renders the shared tail for every add mode: record the project
// for cache pruning, refresh AGENTS.md, then emit the JSON envelope or the
// text success/hint output. firstErr drives the exit-1 sentinel contract.
func emitAddResults(results []*skill.InstallResult, blocked []blockedSkillJSON, firstErr error, projectRoot, lockPath string) error {
	// Record the project so `qvr cache prune` knows this lock is reachable.
	registry.TouchProject(lockPath)

	if !addGlobal {
		refreshAgentsMDFromLock(projectRoot)
	}

	if printer.Format == output.FormatJSON {
		payload := buildAddJSONEnvelope(results, blocked, firstErr)
		if jerr := printer.JSON(payload); jerr != nil {
			return jerr
		}
		if firstErr != nil {
			return errJSONHandled
		}
		return nil
	}
	if firstErr != nil {
		// Per-skill `✗ add <name>: <reason>` lines already surfaced every
		// failure. Returning firstErr would make Cobra's Execute() print
		// `Error: <first reason>` a second time, which CI logs and chats read
		// as "the whole batch failed" even when successes ran (issue #66). The
		// sentinel preserves the exit-1 contract without the duplicate.
		return errTextHandled
	}
	// Next-step hint, init.go-style. Only when at least one skill landed;
	// otherwise a no-op rerun stays quiet. Project installs get the
	// "commit your lockfile" nudge because reproducibility is the
	// whole point of qvr.lock; global installs get the inspection hint.
	if len(results) > 0 {
		if addGlobal {
			printer.Hint("`qvr list --global` shows what's installed in the ambient lane")
		} else {
			printer.Hint("commit qvr.lock so teammates reproduce the same skills (`git add qvr.lock`)")
		}
	}
	return nil
}

// addJSONEnvelope is the stable shape emitted by `qvr add --output json`.
//
// Issue #121: pre-fix the envelope always emitted `installed: []` even on a
// total-failure run, while every other command (read, list, edit, …) emitted
// just `{"error": "..."}`. A consumer walking the CLI couldn't write one error
// handler — it had to branch on the per-command shape. Add now follows the
// universal rule: the `installed` array is only present when at least one
// install attempt produced a result (success or partial-success); pure-error
// runs emit `{"error": "..."}` like the rest of the CLI.
type addJSONEnvelope struct {
	Installed []*skill.InstallResult `json:"installed,omitempty"`
	// Blocked enumerates every skill the scan gate rejected, not just the
	// first. Issue #214: a batch (`add --all` / multi-skill add) that blocks
	// several skills previously surfaced only firstErr in JSON, so an
	// automated consumer couldn't tell which other skills were skipped — the
	// rest only appeared on text stderr. The array makes the full set
	// machine-readable.
	Blocked []blockedSkillJSON `json:"blocked,omitempty"`
	Error   string             `json:"error,omitempty"`
}

// blockedSkillJSON is the machine-readable record of one scan-blocked skill.
type blockedSkillJSON struct {
	Skill       string `json:"skill"`
	Threshold   string `json:"threshold"`
	MaxSeverity string `json:"maxSeverity,omitempty"`
}

// blockedSkillFromErr condenses a *blockedScanError into its JSON record.
func blockedSkillFromErr(e *blockedScanError) blockedSkillJSON {
	rec := blockedSkillJSON{Skill: e.Subject, Threshold: string(e.Threshold)}
	if e.Result != nil {
		rec.MaxSeverity = string(e.Result.Summary.MaxSeverity())
	}
	return rec
}

func buildAddJSONEnvelope(results []*skill.InstallResult, blocked []blockedSkillJSON, err error) addJSONEnvelope {
	env := addJSONEnvelope{Installed: results, Blocked: blocked}
	if err != nil {
		env.Error = err.Error()
	}
	return env
}

// addItem is one normalized install target: the skill ref to hand the
// installer (`name` or `name@ref`) plus the registry scope it resolves under
// ("" = every configured registry, the historical default).
type addItem struct {
	skillRef string
	registry string
	// skillPath is the repo-relative skill directory when the spec pinned one
	// (a /blob/ or /tree/ URL, or a deep skill path). It lets the installer
	// resolve that single SKILL.md without indexing the whole registry. Empty
	// for plain names and bare-repo specs.
	skillPath string
}

// firstDuplicateLocalName returns the first skill name that appears more than
// once across the batch's items, or "" when every name is distinct. The local
// name is the skillRef with any `@ref` stripped (skill names never contain '@'),
// matching the lock key the installer would write — so the same name from two
// registries collides too, which is correct since the lock is keyed by name.
func firstDuplicateLocalName(items []addItem) string {
	seen := make(map[string]struct{}, len(items))
	for _, it := range items {
		name, _, _ := strings.Cut(it.skillRef, "@")
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			return name
		}
		seen[name] = struct{}{}
	}
	return ""
}

// resolveAddItems turns the raw `qvr add` positional args into install targets.
// A plain skill name (optionally `@ref`) passes through scoped to the global
// --registry flag. An arg shaped like a remote clone path
// (`[scheme://]host/org/repo[/skill][@ref]`, or scp-style `git@host:org/repo/...`)
// is the one-step install path: its registry is auto-registered (a no-op if
// already configured) and the arg becomes the skill name scoped to that source.
// Registration happens here — outside the project-lock window — because it's a
// registry-level side effect, not a lock mutation.
func resolveAddItems(ctx context.Context, mgr *registry.Manager, args []string) ([]addItem, error) {
	items := make([]addItem, 0, len(args))
	for _, arg := range args {
		cloneURL, skillName, skillPath, ref, ok := parseRemoteSkillSpec(arg)
		if !ok {
			items = append(items, addItem{skillRef: arg, registry: addRegistry})
			continue
		}
		// A pinned skill directory means we can resolve that one SKILL.md
		// directly downstream, so register the clone without building the
		// registry-wide index (the slow part for a 348-skill repo).
		regName, err := ensureRegistryFor(ctx, mgr, cloneURL, skillPath != "")
		if err != nil {
			return nil, err
		}
		if skillName == "" {
			// Bare repo (`host/org/repo`): install the lone skill, or refuse
			// and name the choices when the repo ships several.
			skillName, err = soleSkillName(mgr, regName, arg)
			if err != nil {
				return nil, err
			}
		}
		skillRef := skillName
		if ref != "" {
			skillRef = skillName + "@" + ref
		}
		items = append(items, addItem{skillRef: skillRef, registry: regName, skillPath: skillPath})
	}
	// Reject a batch that names the same skill twice (e.g. `qvr add tdd tdd@v2`,
	// or the same name from two registries). The under-lock install loop defers
	// lock.Write to the end of the batch, so the per-install ref-conflict check —
	// which re-reads the on-disk lock — can't see an entry a sibling item just
	// added in memory; the second install would silently overwrite the first.
	// --force opts into in-place overwrite, so it bypasses this guard. (--all
	// resolves through resolveAllItems, which already yields unique names.)
	if !addForce {
		if dup := firstDuplicateLocalName(items); dup != "" {
			return nil, fmt.Errorf("skill %q named more than once in this add — install it once (pass --force to overwrite, or run separate `qvr add` commands)", dup)
		}
	}
	return items, nil
}

// ensureRegistryFor registers the registry for cloneURL if it isn't already
// configured, returning its inferred `<org>/<repo>` name. An already-registered
// source is reused as-is (no re-clone). The per-skill install scan still runs
// downstream, so we skip the registry-wide scan pass `qvr registry add` does.
func ensureRegistryFor(ctx context.Context, mgr *registry.Manager, cloneURL string, skipIndex bool) (string, error) {
	name := registry.InferRegistryName(cloneURL)
	if name == "" {
		return "", fmt.Errorf("could not infer a registry name from %q", cloneURL)
	}
	cfg, err := config.Load()
	if err != nil {
		return "", fmt.Errorf("load config: %w", err)
	}
	if _, ok := cfg.Registries[name]; ok {
		return name, nil
	}
	// When the add spec pinned a single skill, skip the registry-wide index
	// build — resolving that one SKILL.md doesn't need every skill parsed, and
	// the index is rebuilt lazily on the next list/search. The reported count
	// is unknown (-1) in that mode, so the message omits it.
	reg, err := mgr.AddWithOptions(ctx, name, cloneURL, registry.AddOptions{
		Depth:     registry.DefaultCloneDepth,
		SkipIndex: skipIndex,
	})
	if err != nil {
		return "", fmt.Errorf("auto-register %s: %w", cloneURL, err)
	}
	if printer.Format != output.FormatJSON {
		if reg.SkillCount < 0 {
			printer.Info(fmt.Sprintf("Registered %s as %q", reg.URL, reg.Name))
		} else {
			printer.Info(fmt.Sprintf("Registered %s as %q (%s)", reg.URL, reg.Name, output.Plural(reg.SkillCount, "skill")))
		}
	}
	return name, nil
}

// soleSkillName returns the single skill in a freshly-registered registry, or
// an error listing the candidates when the repo exposes more than one (so a
// bare `host/org/repo` spec stays unambiguous).
func soleSkillName(mgr *registry.Manager, regName, spec string) (string, error) {
	skills, _, err := mgr.Index(regName, registry.RegistryPath(regName))
	if err != nil {
		return "", fmt.Errorf("index %s: %w", regName, err)
	}
	switch len(skills) {
	case 1:
		return skills[0].Name, nil
	case 0:
		return "", fmt.Errorf("registry %s has no installable skills", regName)
	default:
		names := make([]string, len(skills))
		for i, s := range skills {
			names[i] = s.Name
		}
		sort.Strings(names)
		return "", fmt.Errorf("%s exposes %s (%s); name one, e.g. `qvr add %s/%s`",
			regName, output.Plural(len(skills), "skill"), strings.Join(names, ", "), strings.TrimRight(spec, "/"), names[0])
	}
}

// parseRemoteSkillSpec recognizes a one-step install spec and splits it into a
// clone URL for the org/repo, the skill name (empty when the spec points at the
// bare repo), and an optional ref. It accepts:
//
//	[scheme://][user@]host[:port]/org/repo[/skill...][@ref]   (https, http, ssh, git)
//	[user@]host:org/repo[/skill...][@ref]                     (scp-style SSH)
//	host/org/repo/tree/<ref>/<subpath>                        (web "tree" URL)
//	host/org/repo/blob/<ref>/<path>/SKILL.md                  (web "blob" URL)
//
// The authority (user + host + port) is preserved verbatim in the reconstructed
// clone URL so private SSH remotes keep their identity and port. Embedded HTTPS
// credentials are stripped downstream by the registry layer's SanitizeURL.
//
// ok=false means the arg is a plain skill name (no host/path shape) and should
// flow through normal registry resolution. Detection is deliberately
// conservative: an arg with no `/` is always a plain name, and the host
// component must look like one (contain a `.`), so a stray `foo/bar` never
// silently triggers a network clone.
func parseRemoteSkillSpec(arg string) (cloneURL, skillName, skillPath, ref string, ok bool) {
	raw := strings.TrimSpace(arg)
	if raw == "" || !strings.Contains(raw, "/") {
		return "", "", "", "", false
	}

	// Peel a trailing @ref only when the '@' sits after the last '/', so the
	// user@ in a scp-style git@host:... authority isn't mistaken for a ref. An
	// explicit @ref set here wins over any ref carried in a /tree|blob/ path.
	if at := strings.LastIndex(raw, "@"); at > strings.LastIndex(raw, "/") {
		ref = raw[at+1:]
		raw = raw[:at]
	}

	// Split off the authority (kept verbatim) and the org/repo[/...] path.
	scheme, authority, rest, ok := splitRemoteSpecAuthority(raw)
	if !ok {
		return "", "", "", "", false
	}

	// The host (authority minus user@ and :port) must look like one — guards a
	// bare `org/repo/skill` from triggering a clone of a nonexistent remote.
	if !strings.Contains(authorityHost(authority), ".") {
		return "", "", "", "", false
	}

	segs := splitNonEmptyPathSegments(rest)
	if len(segs) < 2 {
		return "", "", "", "", false // need at least org/repo
	}
	org := segs[0]
	repo := strings.TrimSuffix(segs[1], ".git")
	if org == "" || repo == "" {
		return "", "", "", "", false
	}

	// Everything past org/repo names the skill, with the two web-browse shapes
	// handled specially: org/repo/(tree|blob)/<ref>/<subpath...>. Their <ref>
	// becomes the pin (unless an explicit @ref already set one), and the skill
	// name comes from the subpath rather than the literal "tree"/"blob" segment.
	tail := segs[2:]
	sub := tail
	if len(tail) >= 2 && (tail[0] == "tree" || tail[0] == "blob") {
		if ref == "" {
			ref = tail[1]
		}
		sub = tail[2:]
	}
	skillName = skillFromSubpath(sub)
	skillPath = skillDirFromSubpath(sub)

	if scheme == "scp" {
		cloneURL = fmt.Sprintf("%s:%s/%s.git", authority, org, repo)
	} else {
		cloneURL = fmt.Sprintf("%s://%s/%s/%s.git", scheme, authority, org, repo)
	}
	return cloneURL, skillName, skillPath, ref, true
}

// splitNonEmptyPathSegments splits a "/"-delimited path into its non-empty
// segments (collapsing leading/trailing/duplicate slashes).
func splitNonEmptyPathSegments(path string) []string {
	var segs []string
	for p := range strings.SplitSeq(strings.Trim(path, "/"), "/") {
		if p != "" {
			segs = append(segs, p)
		}
	}
	return segs
}

// splitRemoteSpecAuthority peels the scheme + authority off a remote skill spec,
// returning the scheme ("https" default, "scp" for scp-style SSH), the verbatim
// authority (user+host+port), and the remaining org/repo[/...] path. ok=false
// when the spec has no path component after the authority.
func splitRemoteSpecAuthority(raw string) (scheme, authority, rest string, ok bool) {
	scheme = "https"
	rest = raw
	switch {
	case strings.Contains(rest, "://"):
		i := strings.Index(rest, "://")
		scheme = rest[:i]
		rest = rest[i+3:]
		slash := strings.Index(rest, "/")
		if slash < 0 {
			return "", "", "", false
		}
		authority, rest = rest[:slash], rest[slash+1:]
	case isSCPStyleSpec(rest):
		// scp-style [user@]host:org/repo/...
		colon := strings.Index(rest, ":")
		authority, rest = rest[:colon], rest[colon+1:]
		scheme = "scp"
	default:
		slash := strings.Index(rest, "/")
		if slash < 0 {
			return "", "", "", false
		}
		authority, rest = rest[:slash], rest[slash+1:]
	}
	return scheme, authority, rest, true
}

// isSCPStyleSpec reports whether s is the scp-style SSH shorthand
// `[user@]host:path` (as opposed to a `host:port/...` authority, which has no
// user@). We require a `@` before the first `:` so a port number is never
// mistaken for the path separator.
func isSCPStyleSpec(s string) bool {
	colon := strings.Index(s, ":")
	if colon < 0 {
		return false
	}
	at := strings.Index(s, "@")
	return at >= 0 && at < colon
}

// authorityHost returns the bare host from an authority, dropping any leading
// `user@` and trailing `:port` so the "looks like a host" dot-check isn't
// fooled by the username or confused by the port.
func authorityHost(authority string) string {
	h := authority
	if a := strings.LastIndex(h, "@"); a >= 0 {
		h = h[a+1:]
	}
	if c := strings.LastIndex(h, ":"); c >= 0 {
		h = h[:c]
	}
	return h
}

// skillFromSubpath derives the skill name from the path segments under
// org/repo. The deepest segment names the skill, except a trailing SKILL.md
// (from a /blob/.../SKILL.md URL) which defers to its parent directory — the
// skill dir. Returns "" for an empty subpath (a bare-repo spec).
func skillFromSubpath(sub []string) string {
	if len(sub) == 0 {
		return ""
	}
	last := sub[len(sub)-1]
	if strings.EqualFold(last, "SKILL.md") && len(sub) >= 2 {
		return sub[len(sub)-2]
	}
	return last
}

// skillDirFromSubpath returns the repo-relative directory that holds the skill's
// SKILL.md, derived from the path segments under org/repo. A trailing SKILL.md
// segment (from a /blob/.../SKILL.md URL) is dropped to reach its parent dir.
// Returns "" when the subpath is empty (bare repo) or resolves to the repo root
// — those must go through the full index (sole-skill disambiguation / root
// coexistence), not the single-skill fast path. The result feeds
// registry.FindSkillAtPath, so it names a directory, not the SKILL.md file.
func skillDirFromSubpath(sub []string) string {
	if len(sub) == 0 {
		return ""
	}
	if strings.EqualFold(sub[len(sub)-1], "SKILL.md") {
		sub = sub[:len(sub)-1]
	}
	if len(sub) == 0 {
		return ""
	}
	return strings.Join(sub, "/")
}

// skillDirFor returns the absolute path of the SKILL.md-bearing directory
// for an InstallResult. The InstallResult's Worktree is the worktree root;
// for layout-A skills the actual SKILL.md sits at <worktree>/<subpath>, so
// we read the freshly-written lock entry to recover the subpath. Layout-B
// repos (SKILL.md at repo root) have an empty/"." subpath and the worktree
// root itself is the skill dir.
//
// Returns "" only if the entry can't be located — callers treat that as
// "skip the gate" since there's nothing scannable yet.
func skillDirFor(result *skill.InstallResult, lockPath string) string {
	if result == nil || result.Worktree == "" {
		return ""
	}
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return result.Worktree
	}
	return skillDirForEntry(result, lock)
}

// skillDirForEntry is skillDirFor against an already-loaded in-memory lock. The
// batch add loop (runAddInstallLoop) defers lock.Write to the end of the batch,
// so the on-disk qvr.lock is stale mid-loop — re-reading it would miss the entry
// the just-completed install added in memory and fall back to the worktree root,
// scanning the wrong directory for a nested (layout-A) skill. Resolve the subpath
// from the in-memory entry instead.
func skillDirForEntry(result *skill.InstallResult, lock *model.LockFile) string {
	if result == nil || result.Worktree == "" {
		return ""
	}
	entry, err := lock.Get(result.Name)
	if err != nil {
		return result.Worktree
	}
	worktreePath := skill.EntryWorktreePath(entry)
	if entry.Path == "" || entry.Path == "." {
		return worktreePath
	}
	return filepath.Join(worktreePath, entry.Path)
}
