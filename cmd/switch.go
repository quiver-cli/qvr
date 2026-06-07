package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/git"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/astra-sh/qvr/internal/registry"
	"github.com/astra-sh/qvr/internal/skill"
	"github.com/spf13/cobra"
)

// switch is the single "change which commit an installed skill is on" verb. It
// subsumes the former `upgrade` and `pull` commands (issue #160): all three
// re-point a worktree, with heavily overlapping semantics. One implementation,
// three modes. `pull` survives as an alias (via cmd.CalledAs()); the former
// `upgrade` alias was retired so the top-level `qvr upgrade` can mean "update
// the qvr CLI itself" — upgrading a *skill* is `qvr switch <skill> --latest`.
var (
	repointLatest bool
	repointTo     string
	repointTip    bool
	repointGlobal bool
)

var switchCmd = &cobra.Command{
	Use:     "switch <skill> [ref]",
	Aliases: []string{"pull"},
	Short:   "Change which commit an installed skill is on",
	Long: `Re-point one or more installed skills onto a different commit. One verb,
three modes:

  qvr switch <skill> <ref>     move to an explicit branch, tag, or commit
  qvr switch <skill> --latest  move to the latest semver tag in the registry
  qvr switch <skill> --tip     fast-forward the current ref to its upstream tip
  qvr switch --tip [skill...]  fast-forward every (or just the named) skill

'pull' is an alias kept for muscle memory and scripts, sharing this
implementation but defaulting to the --tip mode it always meant:

  qvr pull [skill...]          == qvr switch --tip [skill...]

To move a skill to its latest release, use 'qvr switch <skill> --latest'.
(The top-level 'qvr upgrade' now updates the qvr CLI binary, not a skill.)

A re-point never clobbers local work: --tip refuses to advance a dirty or
diverged worktree, reporting a conflict for you to resolve manually.`,
	Args: cobra.ArbitraryArgs,
	RunE: runSwitch,
}

func init() {
	switchCmd.Flags().BoolVar(&repointLatest, "latest", false,
		"move the skill to the latest semver tag in its registry")
	switchCmd.Flags().StringVar(&repointTo, "to", "", "explicit ref to move to (alias for the positional <ref>)")
	switchCmd.Flags().BoolVar(&repointTip, "tip", false,
		"fast-forward the current ref to its upstream tip (the mode of `qvr pull`)")
	switchCmd.Flags().BoolVar(&repointGlobal, "global", false,
		"operate on the user-global lock file instead of the project lock")
	rootCmd.AddCommand(switchCmd)
}

// repointMode is the resolved operation a `switch`/`pull` invocation maps to.
// Exactly one is chosen from the flags + how the command was called.
type repointMode int

const (
	modeExplicit repointMode = iota // move to an explicit ref (former `switch`)
	modeLatest                      // move to the latest semver tag (former `upgrade`)
	modeTip                         // fast-forward current ref to tip (former `pull`)
)

func runSwitch(cmd *cobra.Command, args []string) error {
	mode, skills, ref, err := resolveRepoint(cmd.CalledAs(), args)
	if err != nil {
		return err
	}
	if mode == modeTip {
		return runTip(cmd, skills)
	}
	// Explicit / latest both re-point a single skill via the Install flow.
	return runRepoint(cmd, mode, skills[0], ref)
}

// tipPullViaInstall advances a worktree-free consume install to the tip of its
// currently-pinned ref by re-materializing through Install (a new SHA-keyed
// content dir plus a symlink repoint), exactly like switch/upgrade. A tag-pinned
// entry is refused with skill.ErrPinnedToTag — moving off a tag is an explicit
// switch/upgrade, never a pull — so the caller can preserve the historical
// "skipped" semantics. Returns the new commit SHA on success. Install persists
// the lock file itself.
func tipPullViaInstall(cmd *cobra.Command, installer *skill.Installer, mgr *registry.Manager, gc git.GitClient, entry *model.LockEntry, projectRoot, lockPath string) (string, error) {
	canonicalName := entry.Name
	aliasFlag := ""
	if entry.Canonical != "" {
		canonicalName = entry.Canonical
		aliasFlag = entry.Name
	}
	// Refuse a tag pin before any network: moving off a tag is an explicit
	// switch/upgrade. The pinned tag already exists locally, so the cached bare
	// clone is authoritative — no refresh needed to decide this.
	if isTagPinnedRef(gc, entry) {
		return "", fmt.Errorf("%w: %s", skill.ErrPinnedToTag, entry.Ref)
	}

	// Refresh the source registry so a freshly-pushed tip is visible to Install
	// (best-effort; offline flows resolve against the cached bare clone).
	maybeRefreshRegistryForSkill(cmd.Context(), mgr, canonicalName, "pull")

	skillRef := canonicalName
	if entry.Ref != "" {
		skillRef = canonicalName + "@" + entry.Ref
	}
	if _, err := installer.Install(skill.InstallRequest{
		Skill:       skillRef,
		Targets:     entry.Targets,
		Global:      repointGlobal,
		ProjectRoot: projectRoot,
		LockPath:    lockPath,
		Force:       true,
		As:          aliasFlag,
	}); err != nil {
		return "", err
	}
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return "", fmt.Errorf("re-read lock: %w", err)
	}
	updated, err := lock.Get(entry.Name)
	if err != nil {
		return "", fmt.Errorf("entry vanished after pull: %w", err)
	}
	return updated.Commit, nil
}

// isTagPinnedRef reports whether entry.Ref names a tag (and not also a branch)
// in the entry's source registry — the case where a pull is refused. Mirrors the
// tag-vs-branch check in Syncer.Pull, but against the bare clone since a
// worktree-free install has no local repo to inspect.
func isTagPinnedRef(gc git.GitClient, entry *model.LockEntry) bool {
	if entry.Ref == "" {
		return false
	}
	repoPath := registry.RegistryPath(entry.Registry)
	tags, _ := gc.ListTags(repoPath)
	isTag := false
	for _, t := range tags {
		if t.Name == entry.Ref {
			isTag = true
			break
		}
	}
	if !isTag {
		return false
	}
	branches, _ := gc.ListBranches(repoPath)
	for _, b := range branches {
		if b.Name == entry.Ref {
			return false
		}
	}
	return true
}

// resolveRepoint maps (how-it-was-called, positional args, flags) onto a single
// mode + targets. The flags win; absent any, the alias name supplies the
// historical default (upgrade→latest, pull→tip, switch→explicit).
func resolveRepoint(calledAs string, args []string) (repointMode, []string, string, error) {
	// Reject contradictory mode selectors up front so a typo fails loudly
	// instead of silently picking one.
	if repointTip && repointLatest {
		return 0, nil, "", errors.New("--tip and --latest are mutually exclusive")
	}
	if repointTip && repointTo != "" {
		return 0, nil, "", errors.New("--tip and --to are mutually exclusive")
	}
	if repointLatest && repointTo != "" {
		return 0, nil, "", errors.New("--latest and --to are mutually exclusive")
	}

	// Decide the mode.
	var mode repointMode
	switch {
	case repointTip:
		mode = modeTip
	case repointLatest:
		mode = modeLatest
	case repointTo != "":
		mode = modeExplicit
	default:
		// No mode flag: fall back to the historical default of the verb
		// the user actually typed.
		switch calledAs {
		case "pull":
			mode = modeTip
		default: // "switch" needs an explicit ref
			mode = modeExplicit
		}
	}

	switch mode {
	case modeTip:
		// Variadic skills; zero means "every installed skill" (resolved
		// against the lock inside runTip).
		return modeTip, args, "", nil
	case modeLatest:
		if len(args) != 1 {
			return 0, nil, "", errors.New("usage: qvr switch <skill> --latest (exactly one skill)")
		}
		return modeLatest, args[:1], "", nil
	default: // modeExplicit
		if repointTo != "" {
			if len(args) != 1 {
				return 0, nil, "", errors.New("usage: qvr switch <skill> --to <ref> (exactly one skill)")
			}
			return modeExplicit, args[:1], repointTo, nil
		}
		if len(args) != 2 {
			return 0, nil, "", errors.New("usage: qvr switch <skill> <ref> (or --latest / --tip / --to)")
		}
		return modeExplicit, args[:1], args[1], nil
	}
}

// runRepoint moves a single skill onto an explicit ref (modeExplicit) or the
// latest semver tag (modeLatest). Both re-run Install with Force=true, which
// builds (or reuses) a worktree at the new SHA's path and leaves any worktree
// at the old SHA in place — projects pinned to the old SHA keep working;
// `qvr cache prune` GCs the orphan.
func runRepoint(cmd *cobra.Command, mode repointMode, name, ref string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), repointGlobal)

	// The user-facing verb tracks the alias the user actually typed, not the
	// resolved mode: `qvr switch foo --latest` reports "switched", while
	// `qvr pull foo` reports "pulled". `action` (error-message + registry-refresh
	// label) follows suit.
	action := "switch"
	verb := "switched"
	if cmd.CalledAs() == "pull" {
		action, verb = "pull", "pulled"
	}

	var (
		updated         *model.LockEntry
		alreadyOnTarget bool
		latestLock      *model.LockFile
	)
	lockErr := model.WithLock(config.Dir(), lockPath, func() error {
		lock, err := model.ReadLockFile(lockPath)
		if err != nil {
			return fmt.Errorf("read lock: %w", err)
		}
		entry, err := lock.Get(name)
		if err != nil {
			return err
		}

		// Aliased installs (qvr add --as) keep the registry-side skill name in
		// entry.Canonical; the lock key is the alias. Index lookups need the
		// canonical name; replay the alias via As so Install rewrites the same
		// lock key instead of creating a new entry under the canonical name.
		canonicalName := name
		aliasFlag := ""
		if entry.Canonical != "" {
			canonicalName = entry.Canonical
			aliasFlag = name
		}

		gc := git.NewGoGitClient()
		wt := git.NewGoGitWorktree()
		mgr := newRegistryManager(gc)
		// Refresh the source registry so a just-published ref/tag is visible to
		// Install — both paths do this now (the old `switch` skipped it and hit
		// "ref not found" on fresh tags; issue #107). Best-effort: offline flows
		// resolve against the cached index.
		maybeRefreshRegistryForSkill(cmd.Context(), mgr, canonicalName, action)

		target := ref
		if mode == modeLatest {
			// Scope tag discovery to the entry's CURRENT source, not the first
			// registry that alphabetically shares the skill name. A skill
			// fork-migrated with `publish --fork --migrate` has its tags on the
			// fork (entry.Source), while the original registry it was first
			// added from may carry none — resolving by name alone dead-ends
			// --latest on every migrated skill (#183). This matches how
			// outdated/provenance/`switch <ref>` already resolve the entry.
			loc, err := mgr.FindSkillForSource(canonicalName, entry.Registry, entry.Source)
			if err != nil {
				return fmt.Errorf("locate skill: %w", err)
			}
			target = skill.LatestSemverTag(loc.Entry.Versions.Tags)
			if target == "" {
				return fmt.Errorf("no semver tags found for %s in registry %s; pass an explicit <ref> (or --to) instead", canonicalName, loc.RegistryName)
			}
		}
		// modeLatest is idempotent: re-running on the tag you're already on is a
		// no-op. modeExplicit always re-materialises (Force) so a user can
		// repair a damaged worktree by switching to the ref it's already on.
		if mode == modeLatest && target == entry.Ref {
			alreadyOnTarget = true
			printer.Info(fmt.Sprintf("%s: already on %s", name, target))
			return nil
		}

		installer := skill.NewInstaller(mgr, wt, gc)
		if _, err := installer.Install(skill.InstallRequest{
			Skill:       canonicalName + "@" + target,
			Targets:     entry.Targets,
			Global:      repointGlobal,
			ProjectRoot: projectRoot,
			LockPath:    lockPath,
			Force:       true,
			As:          aliasFlag,
		}); err != nil {
			return fmt.Errorf("%s: %w", action, err)
		}
		// Re-read so updated reflects what Install just wrote.
		lock, err = model.ReadLockFile(lockPath)
		if err != nil {
			return fmt.Errorf("re-read lock: %w", err)
		}
		updated, err = lock.Get(name)
		if err != nil {
			return fmt.Errorf("entry vanished after %s: %w", action, err)
		}
		latestLock = lock
		return nil
	})
	if lockErr != nil {
		return lockErr
	}
	if alreadyOnTarget {
		return nil
	}
	registry.TouchProject(lockPath)
	// Keep AGENTS.md in sync with the new ref so descriptions and version
	// markers don't drift until the next `qvr sync`. AGENTS.md is
	// project-scoped, so skip the refresh for a global entry.
	if !repointGlobal && latestLock != nil {
		_ = refreshAgentsMDIfPresent(projectRoot, latestLock.Entries())
	}
	if printer.Format == output.FormatJSON {
		return printer.JSON(updated)
	}
	printer.Success(fmt.Sprintf("%s: %s to %s (%s)", updated.Name, verb, updated.Ref, shortHash(updated.Commit)))
	return nil
}

// runTip fast-forwards each named skill's worktree to the tip of its current
// ref (the former `qvr pull`). With no names it pulls every skill in the lock.
// It refuses to clobber local work: a dirty or diverged worktree, or a
// tag-pinned entry, is reported as a refusal that flips the exit code.
func runTip(cmd *cobra.Command, names []string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), repointGlobal)

	var (
		results    []map[string]string
		loopErr    error
		latestLock *model.LockFile
		nothing    bool
		refused    int
	)
	lockErr := model.WithLock(config.Dir(), lockPath, func() error {
		lock, err := model.ReadLockFile(lockPath)
		if err != nil {
			return fmt.Errorf("read lock: %w", err)
		}
		if len(names) == 0 {
			for _, e := range lock.Entries() {
				names = append(names, e.Name)
			}
		}
		if len(names) == 0 {
			nothing = true
			return nil
		}

		gc := git.NewGoGitClient()
		wt := git.NewGoGitWorktree()
		mgr := newRegistryManager(gc)
		installer := skill.NewInstaller(mgr, wt, gc)
		syncer := skill.NewSyncer(wt, gc)
		for _, name := range names {
			// Re-read the lock each iteration. A worktree-free pull advances via
			// Install, which writes the lock file directly, so a single in-memory
			// copy would go stale and clobber prior updates.
			lock, err = model.ReadLockFile(lockPath)
			if err != nil {
				loopErr = fmt.Errorf("read lock: %w", err)
				break
			}
			entry, err := lock.Get(name)
			if err != nil {
				loopErr = fmt.Errorf("%s: %w", name, err)
				break
			}

			// Worktree-free consume install (#204): immutable + SHA-keyed, so
			// "pull to tip" is a re-materialize at the current tip of the pinned
			// ref (new SHA dir + symlink repoint), identical to switch/upgrade —
			// not a git fast-forward. A tag pin is refused, matching the legacy
			// path. Install persists the lock itself.
			if !skill.HasGitDir(skill.EntryWorktreePath(entry)) {
				hash, perr := tipPullViaInstall(cmd, installer, mgr, gc, entry, projectRoot, lockPath)
				if perr != nil {
					if errors.Is(perr, skill.ErrPinnedToTag) {
						printer.Warning(fmt.Sprintf("%s: %v", name, perr))
						results = append(results, map[string]string{"name": name, "status": "skipped", "message": perr.Error()})
						refused++
						continue
					}
					loopErr = fmt.Errorf("pull %s: %w", name, perr)
					break
				}
				printer.Success(fmt.Sprintf("%s: updated to %s", name, shortHash(hash)))
				results = append(results, map[string]string{"name": name, "status": "updated", "commit": hash})
				continue
			}

			// Legacy git worktree: fast-forward in place.
			hash, err := syncer.Pull(cmd.Context(), entry)
			if err != nil {
				// A diverged or tag-pinned entry is a refusal: the requested
				// pull did not happen. Both are diagnostics (→ stderr, never
				// stdout — stdout stays clean for the JSON payload) and both
				// flip the exit code non-zero so a script notices. We
				// `continue` rather than `break` so the remaining named skills
				// still get pulled (AC-LIFE-3 / AC-LIFE-4, #129).
				if errors.Is(err, skill.ErrDivergence) {
					printer.Warning(fmt.Sprintf("%s: %v", name, err))
					results = append(results, map[string]string{"name": name, "status": "conflict", "message": err.Error()})
					refused++
					continue
				}
				if errors.Is(err, skill.ErrPinnedToTag) {
					printer.Warning(fmt.Sprintf("%s: %v", name, err))
					results = append(results, map[string]string{"name": name, "status": "skipped", "message": err.Error()})
					refused++
					continue
				}
				loopErr = fmt.Errorf("pull %s: %w", name, err)
				break
			}
			entry.Commit = hash
			_ = skill.RefreshSubtreeHash(entry)
			lock.Put(entry)
			if werr := lock.Write(); werr != nil {
				loopErr = fmt.Errorf("write lock: %w", werr)
				break
			}
			printer.Success(fmt.Sprintf("%s: updated to %s", name, shortHash(hash)))
			results = append(results, map[string]string{"name": name, "status": "updated", "commit": hash})
		}
		// Re-read for the AGENTS.md refresh / JSON payload below; per-iteration
		// writes (and Install) have already persisted every change.
		if l, lerr := model.ReadLockFile(lockPath); lerr == nil {
			latestLock = l
		}
		return nil
	})
	if lockErr != nil {
		return lockErr
	}
	if nothing {
		printer.Info("No installed skills. Run 'qvr add <skill>' first.")
		return nil
	}
	registry.TouchProject(lockPath)
	if !repointGlobal && latestLock != nil {
		_ = refreshAgentsMDIfPresent(projectRoot, latestLock.Entries())
	}
	if loopErr != nil {
		return loopErr
	}
	if printer.Format == output.FormatJSON {
		if err := printer.JSON(results); err != nil {
			return err
		}
		// The payload already encodes each refusal's status/message; exit
		// non-zero without a second envelope so the stream stays one JSON doc.
		if refused > 0 {
			return errJSONHandled
		}
		return nil
	}
	// Refusals were already printed to stderr per skill; flip the exit code
	// without re-printing (errTextHandled) so a tag-pinned / diverged pull
	// fails loudly instead of exiting 0 (#129).
	if refused > 0 {
		return errTextHandled
	}
	return nil
}
