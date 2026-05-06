package cmd

import (
	"fmt"
	"os"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/skill"
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

	created, err := enableSkill(entry, projectRoot)
	if err != nil {
		return err
	}
	entry.Disabled = false
	lock.Put(entry)
	if err := lock.Write(); err != nil {
		return fmt.Errorf("write lock: %w", err)
	}
	if !enableGlobal {
		_ = refreshAgentsMDIfPresent(projectRoot, lock.Entries())
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
func enableSkill(entry *model.LockEntry, projectRoot string) ([]string, error) {
	target := skill.EffectiveTarget(entry)
	if target == "" {
		return nil, fmt.Errorf("skill %q has no worktree to link to", entry.Name)
	}
	var created []string
	for _, t := range entry.Targets {
		linkPath, err := skill.ResolveTargetPath(t, entry.Name, projectRoot, entry.Global)
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
