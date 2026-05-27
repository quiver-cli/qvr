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

	// Atomic precondition check: refuse to remove anything if any arg is
	// missing from the lock. Mirrors `git rm` — partial execution on a
	// mid-batch error is a footgun for destructive verbs.
	lock, err := model.ReadLockFile(model.DefaultLockPath(projectRoot, config.Dir(), removeGlobal))
	if err != nil {
		return fmt.Errorf("read lock: %w", err)
	}
	var missing []string
	for _, name := range args {
		if _, err := lock.Get(name); err != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("skill(s) not present in lock file: %v (no changes made)", missing)
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
