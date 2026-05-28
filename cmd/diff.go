package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/output"
	"github.com/spf13/cobra"
)

var (
	diffStat   bool
	diffStaged bool
	diffGlobal bool
	diffAll    bool
)

var diffCmd = &cobra.Command{
	Use:   "diff <skill>",
	Short: "Show local worktree changes for an installed skill",
	Long: `Run git diff inside the skill's worktree. Useful for previewing
changes before qvr push without dropping into ~/.quiver/worktrees/.`,
	Args: cobra.ExactArgs(1),
	RunE: runDiff,
}

func init() {
	diffCmd.Flags().BoolVar(&diffStat, "stat", false, "show diffstat summary instead of full patch")
	diffCmd.Flags().BoolVar(&diffStaged, "staged", false, "diff staged changes (--cached)")
	diffCmd.Flags().BoolVar(&diffGlobal, "global", false, "read the user-global lock file instead of the project lock")
	diffCmd.Flags().BoolVar(&diffAll, "all", false, "search both project and global locks (errors when both contain the skill)")
	rootCmd.AddCommand(diffCmd)
}

func runDiff(cmd *cobra.Command, args []string) error {
	name := args[0]
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	locks, err := loadScopedLocks(projectRoot, diffGlobal, diffAll)
	if err != nil {
		return err
	}
	entry, _, err := findEntryAcrossLocks(name, locks)
	if err != nil {
		return err
	}
	if entry.Source == "link" {
		return fmt.Errorf("diff does not apply to link installs; edit %s directly", entry.LinkTarget)
	}

	diff, err := skillDiff(cmd.Context(), entry, diffStaged, diffStat)
	if err != nil {
		return fmt.Errorf("git diff: %w", err)
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(map[string]any{
			"skill":    name,
			"worktree": entry.Worktree,
			"staged":   diffStaged,
			"stat":     diffStat,
			"diff":     string(diff),
		})
	}
	if len(diff) == 0 {
		printer.Info(fmt.Sprintf("No %s changes in %s", changesNoun(diffStaged), name))
		return nil
	}
	_, err = os.Stdout.Write(diff)
	return err
}

// skillDiff shells out to `git diff` inside the skill's worktree. We pass
// `--no-color` so JSON consumers and pipelines never see ANSI escapes; humans
// still get a clean unified diff in text mode.
func skillDiff(ctx context.Context, entry *model.LockEntry, staged, stat bool) ([]byte, error) {
	args := []string{"diff", "--no-color"}
	if staged {
		args = append(args, "--cached")
	}
	if stat {
		args = append(args, "--stat")
	}
	return git.RunInDir(ctx, entry.Worktree, args...)
}

func changesNoun(staged bool) string {
	if staged {
		return "staged"
	}
	return "unstaged"
}
