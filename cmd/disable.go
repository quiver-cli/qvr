package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/quiver-cli/qvr/internal/config"
	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/output"
	"github.com/quiver-cli/qvr/internal/registry"
	"github.com/quiver-cli/qvr/internal/skill"
	"github.com/spf13/cobra"
)

var disableGlobal bool

var disableCmd = &cobra.Command{
	Use:   "disable <skill>",
	Short: "Hide a skill from agents without removing it",
	Long: `Tear down the agent target symlinks for a skill, but keep its
worktree and lock entry. Reverse with qvr enable.`,
	Args: cobra.ExactArgs(1),
	RunE: runDisable,
}

func init() {
	disableCmd.Flags().BoolVar(&disableGlobal, "global", false,
		"operate on the user-global lock file instead of the project lock")
	rootCmd.AddCommand(disableCmd)
}

func runDisable(cmd *cobra.Command, args []string) error {
	name := args[0]
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), disableGlobal)

	var (
		removed    []string
		latestLock *model.LockFile
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
		if entry.Source == "link" {
			return fmt.Errorf("cannot disable link install %q; use qvr remove instead", name)
		}

		// Persist the disabled state before touching symlinks. A mid-flight crash
		// then leaves a consistent "disabled but symlinks may still be present"
		// state that a rerun of `qvr disable` cleans up idempotently. The
		// alternative — remove first, write after — risks losing symlinks while
		// the lock still claims the skill is enabled.
		entry.Disabled = true
		lock.Put(entry)
		if err := lock.Write(); err != nil {
			return fmt.Errorf("write lock: %w", err)
		}
		rs, derr := disableSkill(entry, projectRoot, disableGlobal)
		if derr != nil {
			entry.Disabled = false
			lock.Put(entry)
			if werr := lock.Write(); werr != nil {
				return fmt.Errorf("disable failed (%v) and rollback lock write also failed: %w", derr, werr)
			}
			return derr
		}
		removed = rs
		latestLock = lock
		return nil
	})
	if lockErr != nil {
		return lockErr
	}
	registry.TouchProject(lockPath)
	if !disableGlobal && latestLock != nil {
		_ = refreshAgentsMDIfPresent(projectRoot, latestLock.Entries())
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(map[string]any{
			"skill":            name,
			"disabled":         true,
			"removed_symlinks": removed,
		})
	}
	if len(removed) == 0 {
		printer.Success(fmt.Sprintf("%s already has no active symlinks; marked disabled", name))
		return nil
	}
	printer.Success(fmt.Sprintf("Disabled %s (removed %d symlink(s))", name, len(removed)))
	return nil
}

// disableSkill removes the symlinks for every target on the entry. Missing
// symlinks are ignored — disable is intentionally idempotent so repeating it
// or running it after a manual cleanup is a no-op rather than an error.
func disableSkill(entry *model.LockEntry, projectRoot string, global bool) ([]string, error) {
	var removed []string
	for _, t := range entry.Targets {
		linkPath, err := skill.ResolveTargetPath(t, entry.Name, projectRoot, global)
		if err != nil {
			return removed, fmt.Errorf("resolve %s: %w", t, err)
		}
		if err := skill.RemoveSymlink(linkPath); err != nil {
			if errors.Is(err, skill.ErrSymlinkNotFound) {
				continue
			}
			return removed, fmt.Errorf("remove %s: %w", linkPath, err)
		}
		removed = append(removed, linkPath)
	}
	return removed, nil
}
