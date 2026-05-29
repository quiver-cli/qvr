package cmd

import (
	"fmt"
	"os"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/registry"
	"github.com/raks097/quiver/internal/skill"
	"github.com/spf13/cobra"
)

var (
	editGlobal bool
	editAuthor string
	editEmail  string
)

var editCmd = &cobra.Command{
	Use:   "edit <skill>",
	Short: "Eject a skill into the project so you can modify it",
	Long: `Promote the symlinked skill into a real directory inside the project so
you can edit it directly. The canonical agent target dir (alphabetical-first
installed target, e.g. .claude/skills/<name>/) becomes a real directory
populated from the shared worktree; any other installed target dirs become
relative symlinks pointing at it.

After eject, ` + "`qvr publish <skill>`" + ` ships your edits — either back to the
original upstream (` + "`--tag v1.0.1`" + `) or to a brand-new remote
(` + "`--fork <url> --migrate`" + `).

Idempotent: running ` + "`qvr edit`" + ` again after the first eject is a no-op.`,
	Args: cobra.ExactArgs(1),
	RunE: runEdit,
}

func init() {
	editCmd.Flags().BoolVar(&editGlobal, "global", false, "operate on the user-global lock file instead of the project lock")
	editCmd.Flags().StringVar(&editAuthor, "author", "", "git author for the initial commit (defaults to 'quiver')")
	editCmd.Flags().StringVar(&editEmail, "email", "", "git author email for the initial commit (defaults to 'quiver@localhost')")
	rootCmd.AddCommand(editCmd)
}

func runEdit(cmd *cobra.Command, args []string) error {
	name := args[0]
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), editGlobal)

	var (
		result       *skill.EjectResult
		updatedEntry *model.LockEntry
		latestLock   *model.LockFile
		idempotent   bool
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
		// Surface a friendlier error than EjectToTarget's generic refusal —
		// users editing a link install are usually unaware that the link is
		// already locally owned.
		if entry.IsLink() {
			return fmt.Errorf("%s is a link install at %s — edit the source path directly", name, entry.Source)
		}
		// Already ejected: no-op, return current state.
		if entry.IsEdit() {
			idempotent = true
			updatedEntry = entry
			return nil
		}

		r, err := skill.EjectToTarget(skill.EjectRequest{
			Entry:       entry,
			ProjectRoot: projectRoot,
			Global:      editGlobal,
			Author:      editAuthor,
			AuthorEmail: editEmail,
		})
		if err != nil {
			return fmt.Errorf("edit %s: %w", name, err)
		}
		// EjectToTarget mutated entry; persist.
		lock.Put(entry)
		if err := lock.Write(); err != nil {
			return fmt.Errorf("write lock: %w", err)
		}
		result = r
		updatedEntry = entry
		latestLock = lock
		return nil
	})
	if lockErr != nil {
		return lockErr
	}

	registry.TouchProject(lockPath)
	if !editGlobal && latestLock != nil {
		_ = refreshAgentsMDIfPresent(projectRoot, latestLock.Entries())
	}

	if printer.Format == output.FormatJSON {
		payload := map[string]any{
			"skill":      updatedEntry,
			"idempotent": idempotent,
		}
		if result != nil {
			payload["eject"] = result
		}
		return printer.JSON(payload)
	}
	if idempotent {
		printer.Info(fmt.Sprintf("%s: already ejected at %s", updatedEntry.Name, updatedEntry.EditPath))
		return nil
	}
	printer.Success(fmt.Sprintf("%s: ejected to %s — edit files there, then `qvr publish %s --tag <ver>`", updatedEntry.Name, updatedEntry.EditPath, updatedEntry.Name))
	if len(result.SiblingLinks) > 0 {
		printer.Info(fmt.Sprintf("  repointed %d sibling target symlink(s)", len(result.SiblingLinks)))
	}
	return nil
}
