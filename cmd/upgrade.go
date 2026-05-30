package cmd

import (
	"fmt"
	"os"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/registry"
	"github.com/raks097/quiver/internal/skill"
	"github.com/spf13/cobra"
)

var (
	upgradeTo     string
	upgradeGlobal bool
)

var upgradeCmd = &cobra.Command{
	Use:   "upgrade <skill>",
	Short: "Move a skill to the latest semver tag (or an explicit ref)",
	Long: `Resolve the latest semver tag for the skill's registry and switch the
worktree onto it. Use --to <ref> to pin a specific branch, tag, or commit.

If the registry has no semver tags and --to is not set, upgrade exits with an
error — in that case use 'qvr switch' or 'qvr pull' instead.`,
	Args: cobra.ExactArgs(1),
	RunE: runUpgrade,
}

func init() {
	upgradeCmd.Flags().StringVar(&upgradeTo, "to", "", "ref to upgrade to (defaults to latest semver tag)")
	upgradeCmd.Flags().BoolVar(&upgradeGlobal, "global", false, "operate on the user-global lock file instead of the project lock")
	rootCmd.AddCommand(upgradeCmd)
}

func runUpgrade(cmd *cobra.Command, args []string) error {
	name := args[0]
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), upgradeGlobal)

	var (
		updated         *model.LockEntry
		alreadyOnTarget bool
		latestLock      *model.LockFile
	)
	lockErr := model.WithLock(lockPath, func() error {
		lock, err := model.ReadLockFile(lockPath)
		if err != nil {
			return fmt.Errorf("read lock: %w", err)
		}
		entry, err := lock.Get(name)
		if err != nil {
			return err
		}

		// Aliased installs (qvr add --as) keep the registry-side skill
		// name in entry.Canonical; the lock key is the alias. Index
		// lookups (FindSkill, Update) need the canonical name; the
		// alias is only meaningful to the local lockfile.
		canonicalName := name
		if entry.Canonical != "" {
			canonicalName = entry.Canonical
		}
		target := upgradeTo
		if target == "" {
			mgr := newRegistryManager(git.NewGoGitClient())
			loc, err := mgr.FindSkill(canonicalName)
			if err != nil {
				return fmt.Errorf("locate skill: %w", err)
			}
			// Refresh the source registry before consulting tags so a
			// just-published v0.2.0 is visible without first running
			// `qvr registry update`. Without this, the TTL'd index cache
			// makes upgrade lie about the latest version while `qvr
			// outdated` (which ls-remotes) sees the new tag — the
			// "feels broken on first try" finding from the OSS-readiness
			// audit. Network failure is non-fatal: fall through with the
			// stale index so offline workflows still resolve to the best
			// known tag.
			if _, uerr := mgr.Update(cmd.Context(), loc.RegistryName); uerr != nil {
				printer.Warning(fmt.Sprintf("upgrade: refresh %s failed (%v); using cached tags", loc.RegistryName, uerr))
			} else {
				// Re-read with the refreshed index so the new tags land
				// in loc.Entry.Versions.Tags.
				if refreshed, rerr := mgr.FindSkill(canonicalName); rerr == nil {
					loc = refreshed
				}
			}
			target = skill.LatestSemverTag(loc.Entry.Versions.Tags)
			if target == "" {
				return fmt.Errorf("no semver tags found for %s in registry %s; pass --to <ref> to pick manually", canonicalName, loc.RegistryName)
			}
		}
		if target == entry.Ref {
			alreadyOnTarget = true
			printer.Info(fmt.Sprintf("%s: already on %s", name, target))
			return nil
		}

		// SHA-keyed upgrade: same machinery as switch — Install at the new
		// ref builds a fresh worktree under the new SHA's path and leaves
		// any existing worktree at the old SHA in place. Shared worktrees
		// across projects survive other projects upgrading off them; the
		// orphans get cleaned by `qvr cache prune`.
		//
		// For aliased entries we pass As=name (the alias) so Install
		// rewrites the same lock key instead of creating a new entry
		// under the canonical name.
		aliasFlag := ""
		if entry.Canonical != "" {
			aliasFlag = name
		}
		gcc := git.NewGoGitClient()
		wt := git.NewGoGitWorktree()
		installer := skill.NewInstaller(newRegistryManager(gcc), wt, gcc)
		if _, err := installer.Install(skill.InstallRequest{
			Skill:       canonicalName + "@" + target,
			Targets:     entry.Targets,
			Global:      upgradeGlobal,
			ProjectRoot: projectRoot,
			LockPath:    lockPath,
			Force:       true,
			As:          aliasFlag,
		}); err != nil {
			return fmt.Errorf("upgrade: %w", err)
		}
		// Re-read so updated reflects what Install just wrote.
		lock, err = model.ReadLockFile(lockPath)
		if err != nil {
			return fmt.Errorf("re-read lock: %w", err)
		}
		updated, err = lock.Get(name)
		if err != nil {
			return fmt.Errorf("entry vanished after upgrade: %w", err)
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
	if !upgradeGlobal && latestLock != nil {
		_ = refreshAgentsMDIfPresent(projectRoot, latestLock.Entries())
	}
	if printer.Format == output.FormatJSON {
		return printer.JSON(updated)
	}
	printer.Success(fmt.Sprintf("%s: upgraded to %s (%s)", updated.Name, updated.Ref, shortHash(updated.Commit)))
	return nil
}
