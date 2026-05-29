package cmd

import (
	"errors"
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
	pushMessage string
	pushAuthor  string
	pushEmail   string
	pushGlobal  bool
)

var pushCmd = &cobra.Command{
	Use:   "push <skill>",
	Short: "Commit and push local changes upstream",
	Long: `Stage all changes in the worktree, commit with --message, and push to origin.
Nothing is pushed when the worktree is clean.`,
	Args: cobra.ExactArgs(1),
	RunE: runPush,
}

func init() {
	pushCmd.Flags().StringVarP(&pushMessage, "message", "m", "", "commit message")
	pushCmd.Flags().StringVar(&pushAuthor, "author", "", "commit author name")
	pushCmd.Flags().StringVar(&pushEmail, "email", "", "commit author email")
	pushCmd.Flags().BoolVar(&pushGlobal, "global", false, "read the user-global lock file instead of the project lock")
	rootCmd.AddCommand(pushCmd)
}

func runPush(cmd *cobra.Command, args []string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), pushGlobal)

	var (
		pushedEntry *model.LockEntry
		pushedHash  string
		noChanges   bool
		warning     string
	)
	lockErr := model.WithLock(lockPath, func() error {
		lock, err := model.ReadLockFile(lockPath)
		if err != nil {
			return fmt.Errorf("read lock: %w", err)
		}
		entry, err := lock.Get(args[0])
		if err != nil {
			return fmt.Errorf("lookup %s: %w", args[0], err)
		}

		gc := git.NewGoGitClient()
		if entry.Registry != "" {
			if defaultBranch, err := gc.DefaultBranch(registry.RegistryPath(entry.Registry)); err == nil && defaultBranch != "" && defaultBranch == entry.Ref {
				warning = fmt.Sprintf("%s is on registry default branch %q — this push targets the shared upstream. Use 'qvr edit' first if you want to land on your own branch.",
					entry.Name, entry.Ref)
			}
		}

		syncer := skill.NewSyncer(git.NewGoGitWorktree(), gc)
		hash, err := syncer.Push(cmd.Context(), entry, skill.PushOptions{
			Message:     pushMessage,
			Author:      pushAuthor,
			AuthorEmail: pushEmail,
		})
		if err != nil {
			if errors.Is(err, skill.ErrPushNoChanges) {
				noChanges = true
				pushedEntry = entry
				return nil
			}
			return fmt.Errorf("push %s: %w", entry.Name, err)
		}
		entry.Commit = hash
		lock.Put(entry)
		if err := lock.Write(); err != nil {
			return fmt.Errorf("write lock: %w", err)
		}
		pushedEntry = entry
		pushedHash = hash
		return nil
	})
	if lockErr != nil {
		return lockErr
	}
	registry.TouchProject(lockPath)
	if warning != "" {
		printer.Warning(warning)
	}
	if noChanges {
		if printer.Format == output.FormatJSON {
			return printer.JSON(map[string]string{
				"name":    pushedEntry.Name,
				"status":  "no-op",
				"message": "no local changes",
			})
		}
		printer.Info(fmt.Sprintf("%s: no local changes", pushedEntry.Name))
		return nil
	}
	if printer.Format == output.FormatJSON {
		return printer.JSON(map[string]string{
			"name":   pushedEntry.Name,
			"status": "pushed",
			"commit": pushedHash,
		})
	}
	printer.Success(fmt.Sprintf("%s: pushed %s", pushedEntry.Name, shortHash(pushedHash)))
	return nil
}
