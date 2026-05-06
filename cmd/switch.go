package cmd

import (
	"fmt"
	"os"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/skill"
	"github.com/spf13/cobra"
)

var switchGlobal bool

var switchCmd = &cobra.Command{
	Use:   "switch <skill> <ref>",
	Short: "Switch a skill's worktree to a different branch or tag",
	Args:  cobra.ExactArgs(2),
	RunE:  runSwitch,
}

func init() {
	switchCmd.Flags().BoolVar(&switchGlobal, "global", false,
		"operate on the user-global lock file instead of the project lock")
	rootCmd.AddCommand(switchCmd)
}

func runSwitch(cmd *cobra.Command, args []string) error {
	name, ref := args[0], args[1]
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	lock, err := model.ReadLockFile(model.DefaultLockPath(projectRoot, config.Dir(), switchGlobal))
	if err != nil {
		return fmt.Errorf("read lock: %w", err)
	}
	entry, err := lock.Get(name)
	if err != nil {
		return err
	}
	// Perform the git switch first so a bad ref doesn't leave renamed dirs behind.
	syncer := skill.NewSyncer(git.NewGoGitWorktree(), git.NewGoGitClient())
	updated, err := syncer.Switch(cmd.Context(), entry, ref)
	if err != nil {
		return fmt.Errorf("switch: %w", err)
	}
	if err := skill.ApplySwitch(updated, projectRoot); err != nil {
		return err
	}

	lock.Put(updated)
	if err := lock.Write(); err != nil {
		return fmt.Errorf("write lock: %w", err)
	}
	// Keep AGENTS.md in sync with the new ref so descriptions and version
	// markers don't drift until the user next runs `qvr sync`. AGENTS.md is
	// project-scoped, so skip the refresh when switching a global entry.
	if !switchGlobal {
		_ = refreshAgentsMDIfPresent(projectRoot, lock.Entries())
	}
	if printer.Format == output.FormatJSON {
		return printer.JSON(updated)
	}
	printer.Success(fmt.Sprintf("%s: switched to %s (%s)", updated.Name, updated.Branch, shortHash(updated.Commit)))
	return nil
}
