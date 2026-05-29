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
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), switchGlobal)

	var (
		updated    *model.LockEntry
		latestLock *model.LockFile
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
		// SHA-keyed switch: install the skill again at the new ref via the
		// standard Install flow with Force=true. That creates (or reuses) a
		// worktree at the new SHA's path and leaves any existing worktree at
		// the old SHA untouched — projects pinned to the old SHA keep working.
		// `qvr cache prune` is the GC for the now-orphan old worktree.
		gc := git.NewGoGitClient()
		wt := git.NewGoGitWorktree()
		installer := skill.NewInstaller(newRegistryManager(gc), wt, gc)
		result, err := installer.Install(skill.InstallRequest{
			Skill:       name + "@" + ref,
			Targets:     entry.Targets,
			Global:      switchGlobal,
			ProjectRoot: projectRoot,
			LockPath:    lockPath,
			Force:       true,
		})
		if err != nil {
			return fmt.Errorf("switch: %w", err)
		}
		// Re-read so we have the freshly-written entry — Install just wrote it.
		lock, err = model.ReadLockFile(lockPath)
		if err != nil {
			return fmt.Errorf("re-read lock: %w", err)
		}
		updated, err = lock.Get(name)
		if err != nil {
			return fmt.Errorf("entry vanished after install: %w", err)
		}
		_ = result
		latestLock = lock
		return nil
	})
	if lockErr != nil {
		return lockErr
	}
	registry.TouchProject(lockPath)
	// Keep AGENTS.md in sync with the new ref so descriptions and version
	// markers don't drift until the user next runs `qvr sync`. AGENTS.md is
	// project-scoped, so skip the refresh when switching a global entry.
	if !switchGlobal && latestLock != nil {
		_ = refreshAgentsMDIfPresent(projectRoot, latestLock.Entries())
	}
	if printer.Format == output.FormatJSON {
		return printer.JSON(updated)
	}
	printer.Success(fmt.Sprintf("%s: switched to %s (%s)", updated.Name, updated.Ref, shortHash(updated.Commit)))
	return nil
}
