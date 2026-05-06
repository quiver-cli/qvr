package cmd

import (
	"fmt"
	"os"

	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/registry"
	"github.com/raks097/quiver/internal/skill"
	"github.com/spf13/cobra"
)

var removeGlobal bool

var removeCmd = &cobra.Command{
	Use:   "remove <skill>...",
	Short: "Remove installed skills",
	Long: `Remove symlinks, worktree, and lock entry for one or more installed skills.
Pass --global to operate on the user-global lock file (mirrors ` + "`qvr install --global`" + `).`,
	Args: cobra.MinimumNArgs(1),
	RunE: runRemove,
}

func init() {
	removeCmd.Flags().BoolVar(&removeGlobal, "global", false,
		"operate on the user-global lock file instead of the project lock")
	rootCmd.AddCommand(removeCmd)
}

func runRemove(cmd *cobra.Command, args []string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	gc := git.NewGoGitClient()
	wt := git.NewGoGitWorktree()
	installer := skill.NewInstaller(registry.NewManager(gc), wt, gc)

	var removed []string
	for _, name := range args {
		req := skill.InstallRequest{ProjectRoot: projectRoot, Global: removeGlobal}
		if err := installer.Remove(name, req); err != nil {
			if len(removed) > 0 && !removeGlobal {
				refreshAgentsMDFromLock(projectRoot)
			}
			return fmt.Errorf("remove %s: %w", name, err)
		}
		removed = append(removed, name)
	}
	if !removeGlobal {
		refreshAgentsMDFromLock(projectRoot)
	}
	if printer.Format == output.FormatJSON {
		return printer.JSON(map[string]any{"removed": removed})
	}
	for _, n := range removed {
		printer.Success(fmt.Sprintf("Removed %s", n))
	}
	return nil
}
