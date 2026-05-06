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
	lock, err := model.ReadLockFile(model.DefaultLockPath(projectRoot, config.Dir(), upgradeGlobal))
	if err != nil {
		return fmt.Errorf("read lock: %w", err)
	}
	entry, err := lock.Get(name)
	if err != nil {
		return err
	}

	target := upgradeTo
	if target == "" {
		mgr := registry.NewManager(git.NewGoGitClient())
		loc, err := mgr.FindSkill(name)
		if err != nil {
			return fmt.Errorf("locate skill: %w", err)
		}
		target = skill.LatestSemverTag(loc.Entry.Versions.Tags)
		if target == "" {
			return fmt.Errorf("no semver tags found for %s in registry %s; pass --to <ref> to pick manually", name, loc.RegistryName)
		}
	}
	if target == entry.Branch {
		printer.Info(fmt.Sprintf("%s: already on %s", name, target))
		return nil
	}

	syncer := skill.NewSyncer(git.NewGoGitWorktree(), git.NewGoGitClient())
	updated, err := syncer.Switch(cmd.Context(), entry, target)
	if err != nil {
		return fmt.Errorf("upgrade: %w", err)
	}
	if err := skill.ApplySwitch(updated, projectRoot); err != nil {
		return err
	}

	lock.Put(updated)
	if err := lock.Write(); err != nil {
		return fmt.Errorf("write lock: %w", err)
	}
	if !upgradeGlobal {
		_ = refreshAgentsMDIfPresent(projectRoot, lock.Entries())
	}
	if printer.Format == output.FormatJSON {
		return printer.JSON(updated)
	}
	printer.Success(fmt.Sprintf("%s: upgraded to %s (%s)", updated.Name, updated.Branch, shortHash(updated.Commit)))
	return nil
}
