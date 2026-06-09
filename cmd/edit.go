package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/astra-sh/qvr/internal/registry"
	"github.com/astra-sh/qvr/internal/skill"
	"github.com/spf13/cobra"
)

var (
	editAuthor string
	editEmail  string
)

var editCmd = &cobra.Command{
	Use:   "edit <skill>",
	Short: "Eject a project-local skill so you can modify it",
	Long: `Promote the symlinked skill into a real directory inside the project so
you can edit it directly. The canonical agent target dir (alphabetical-first
installed target, e.g. .claude/skills/<name>/) becomes a real directory
populated from the shared worktree; any other installed target dirs become
relative symlinks pointing at it.

After eject, ` + "`qvr publish <skill>`" + ` ships your edits — either back to the
original upstream (` + "`--tag v1.0.1`" + `) or to a brand-new remote
(` + "`--fork <url> --migrate`" + `).

` + "`qvr edit`" + ` only ejects **project-local** skills. Editing a globally
installed skill in place would mutate a shared copy that every project sees, so
it is not supported. To change a global skill: add it to a project
(` + "`qvr add <skill>`" + `), ` + "`qvr edit`" + ` and ` + "`qvr publish`" + ` your
changes there, then re-add the published version globally
(` + "`qvr add <skill> --global`" + `).

Idempotent: running ` + "`qvr edit`" + ` again after the first eject is a no-op.`,
	Args: cobra.ExactArgs(1),
	RunE: runEdit,
}

func init() {
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
	// edit is project-local only: ejecting a global skill in place would mutate
	// the single copy every project shares. The global lock is read solely to
	// give a precise "use publish then re-add globally" error below.
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), false)
	projPath := model.DefaultProjectPath(projectRoot)

	var (
		result       *skill.EjectResult
		updatedEntry *model.LockEntry
		latestLock   *model.LockFile
		idempotent   bool
	)
	lockErr := model.WithLock(config.Dir(), lockPath, func() error {
		return ejectUnderLock(name, projectRoot, lockPath, projPath, &result, &updatedEntry, &latestLock, &idempotent)
	})
	if lockErr != nil {
		return lockErr
	}

	registry.TouchProject(lockPath)
	if latestLock != nil {
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
	printer.Success(fmt.Sprintf("Ejected %s to %s — edit files there, then `qvr publish %s --tag <ver>`", updatedEntry.Name, updatedEntry.EditPath, updatedEntry.Name))
	if len(result.SiblingLinks) > 0 {
		printer.Detail(fmt.Sprintf("repointed %s", output.Plural(len(result.SiblingLinks), "sibling target symlink")))
	}
	return nil
}

// ejectUnderLock performs the lock-held portion of runEdit: read the entry,
// reject link/global skills, and (unless already ejected) eject the skill into
// a real project dir, persisting the lock and dropping its qvr.toml coordinate.
// Results flow back through the out-pointers.
func ejectUnderLock(name, projectRoot, lockPath, projPath string, result **skill.EjectResult, updatedEntry **model.LockEntry, latestLock **model.LockFile, idempotent *bool) error {
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return fmt.Errorf("read lock: %w", err)
	}
	entry, err := lock.Get(name)
	if err != nil {
		if errors.Is(err, model.ErrLockSkillMissing) && installedGlobally(projectRoot, name) {
			return fmt.Errorf("%s is installed globally, not in this project — `qvr edit` only ejects project-local skills; "+
				"to change it, `qvr add %s` here, edit & `qvr publish`, then re-add it globally with `qvr add %s --global`", name, name, name)
		}
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
		*idempotent = true
		*updatedEntry = entry
		return nil
	}

	// Capture the qvr.toml coordinate while the entry is still shared-mode —
	// EjectToTarget flips it to Mode=edit (coordinate becomes ""), and an
	// ejected skill is lock-only, so we drop its declarative entry below.
	coord := model.SkillCoordinate(entry)

	r, err := skill.EjectToTarget(skill.EjectRequest{
		Entry:       entry,
		ProjectRoot: projectRoot,
		Global:      false,
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
	// Write-through: an ejected skill is reproduced from the lock (Mode=edit),
	// so it leaves qvr.toml's registry-sourced [skills] table.
	if coord != "" {
		if perr := dropProjectFileSkills(projPath, []string{coord}); perr != nil {
			printer.Warning(fmt.Sprintf("ejected in qvr.lock but failed to update qvr.toml (%v); run `qvr sync` to reconcile", perr))
		}
	}
	*result = r
	*updatedEntry = entry
	*latestLock = lock
	return nil
}

// installedGlobally reports whether name is present in the user-global lock.
// Used only to turn a project-lock "not found" into an actionable message that
// steers the user to the publish → re-add-globally workflow instead of editing
// a shared global copy in place. Best-effort: any read error means "can't
// confirm", so the caller falls back to the plain not-found error.
func installedGlobally(projectRoot, name string) bool {
	globalLock, err := model.ReadLockFile(model.DefaultLockPath(projectRoot, config.Dir(), true))
	if err != nil {
		return false
	}
	_, err = globalLock.Get(name)
	return err == nil
}
