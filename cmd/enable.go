package cmd

import (
	"fmt"
	"os"

	"github.com/quiver-cli/qvr/internal/config"
	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/output"
	"github.com/quiver-cli/qvr/internal/registry"
	"github.com/quiver-cli/qvr/internal/skill"
	"github.com/spf13/cobra"
)

var enableGlobal bool

var enableCmd = &cobra.Command{
	Use:   "enable <skill>",
	Short: "Re-create symlinks for a previously disabled skill",
	Args:  cobra.ExactArgs(1),
	RunE:  runEnable,
}

func init() {
	enableCmd.Flags().BoolVar(&enableGlobal, "global", false,
		"operate on the user-global lock file instead of the project lock")
	rootCmd.AddCommand(enableCmd)
}

func runEnable(cmd *cobra.Command, args []string) error {
	name := args[0]
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), enableGlobal)

	var (
		created    []string
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
			return fmt.Errorf("cannot enable link install %q", name)
		}

		cs, eerr := enableSkill(entry, projectRoot, enableGlobal)
		if eerr != nil {
			return eerr
		}
		entry.Disabled = false
		lock.Put(entry)
		if err := lock.Write(); err != nil {
			return fmt.Errorf("write lock: %w", err)
		}
		created = cs
		latestLock = lock
		return nil
	})
	if lockErr != nil {
		return lockErr
	}
	registry.TouchProject(lockPath)
	if !enableGlobal && latestLock != nil {
		_ = refreshAgentsMDIfPresent(projectRoot, latestLock.Entries())
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(map[string]any{
			"skill":            name,
			"disabled":         false,
			"created_symlinks": created,
		})
	}
	printer.Success(fmt.Sprintf("Enabled %s (linked %d target(s))", name, len(created)))
	return nil
}

// enableSkill re-creates symlinks for each declared target. Idempotent:
// CreateSymlink returns nil for symlinks already pointing at the worktree.
func enableSkill(entry *model.LockEntry, projectRoot string, global bool) ([]string, error) {
	// Link to the sanitized view for a consumed root-layout skill so .git isn't
	// exposed (issue #154); a no-op passthrough for every other shape.
	target, err := skill.MaterializeAgentView(entry, projectRoot)
	if err != nil {
		return nil, fmt.Errorf("agent view: %w", err)
	}
	if target == "" {
		return nil, fmt.Errorf("skill %q has no worktree to link to", entry.Name)
	}
	var created []string
	for _, t := range entry.Targets {
		linkPath, err := skill.ResolveTargetPath(t, entry.Name, projectRoot, global)
		if err != nil {
			return created, fmt.Errorf("resolve %s: %w", t, err)
		}
		if err := skill.CreateSymlink(linkPath, target); err != nil {
			return created, fmt.Errorf("link %s: %w", linkPath, err)
		}
		created = append(created, linkPath)
	}
	return created, nil
}
