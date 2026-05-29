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
	removeGlobal bool
	removeForce  bool
)

var removeCmd = &cobra.Command{
	Use:   "remove <skill>...",
	Short: "Remove installed skills",
	Long: `Remove symlinks, worktree, and lock entry for one or more installed skills.
Pass --global to operate on the user-global lock file (mirrors ` + "`qvr add --global`" + `).
Pass --force to delete a mode:edit eject dir on disk (user edits live there —
without --force, qvr refuses to touch the eject dir; issue #93).`,
	Args: cobra.MinimumNArgs(1),
	RunE: runRemove,
}

func init() {
	removeCmd.Flags().BoolVar(&removeGlobal, "global", false,
		"operate on the user-global lock file instead of the project lock")
	removeCmd.Flags().BoolVar(&removeForce, "force", false,
		"delete the mode:edit eject dir on disk (user edits are otherwise sacred)")
	rootCmd.AddCommand(removeCmd)
}

func runRemove(cmd *cobra.Command, args []string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}

	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), removeGlobal)

	var removed []string
	lockErr := model.WithLock(lockPath, func() error {
		// Atomic precondition check: refuse to remove anything if any arg is
		// missing from the lock. Mirrors `git rm` — partial execution on a
		// mid-batch error is a footgun for destructive verbs.
		lock, err := model.ReadLockFile(lockPath)
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

		// Mode:edit pre-flight: refuse without --force, since the eject dir
		// holds user edits that are not recoverable from upstream. Issue #93.
		if !removeForce {
			var ejected []string
			for _, name := range args {
				e, _ := lock.Get(name)
				if e != nil && e.IsEdit() {
					ejected = append(ejected, name)
				}
			}
			if len(ejected) > 0 {
				return fmt.Errorf("refuse to remove ejected skill(s) %v: the eject dir(s) at <projectRoot>/<EditPath> hold local edits not recoverable from upstream. Pass --force to delete them, or publish first (`qvr publish <skill>`)", ejected)
			}
		}

		gc := git.NewGoGitClient()
		wt := git.NewGoGitWorktree()
		installer := skill.NewInstaller(newRegistryManager(gc), wt, gc)

		for _, name := range args {
			req := skill.InstallRequest{ProjectRoot: projectRoot, Global: removeGlobal, Force: removeForce}
			if err := installer.Remove(name, req); err != nil {
				return fmt.Errorf("remove %s: %w", name, err)
			}
			removed = append(removed, name)
		}
		return nil
	})
	if lockErr != nil {
		if len(removed) > 0 && !removeGlobal {
			refreshAgentsMDFromLock(projectRoot)
		}
		return lockErr
	}
	registry.TouchProject(lockPath)
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
