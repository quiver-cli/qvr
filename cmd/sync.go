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
	syncGlobal        bool
	syncDryRun        bool
	syncKeepUntracked bool
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Reconcile the project against qvr.lock",
	Long: `Make the on-disk state match the lock file. For every entry in the
lock, ensure its worktree exists in the shared cache and the agent-target
symlinks point at it. Then strict-remove any symlinks under managed agent
directories (.claude/skills/, .cursor/rules/, etc.) whose target is a
qvr-managed cache path but which don't appear in the lock — that's the
"hidden by default" guarantee.

A symlink whose target sits outside the qvr-managed scope (e.g. into your
own dev directory or somewhere weirder like /etc/passwd) is left alone
and surfaced in the output so you can investigate; sync never removes
anything we don't recognise as ours.

Pass --global to reconcile against the user-global lock at ~/.quiver/qvr.lock.
Pass --dry-run to see what would change without touching the filesystem.
Pass --keep-untracked to downgrade orphan removal to a warning — handy
when you mix hand-managed skills with qvr-managed ones in the same dir.`,
	RunE: runSync,
}

func init() {
	syncCmd.Flags().BoolVar(&syncGlobal, "global", false,
		"reconcile against the user-global lock instead of the project lock")
	syncCmd.Flags().BoolVar(&syncDryRun, "dry-run", false,
		"report what would change without touching the filesystem")
	syncCmd.Flags().BoolVar(&syncKeepUntracked, "keep-untracked", false,
		"warn about orphan managed symlinks instead of removing them")
	rootCmd.AddCommand(syncCmd)
}

func runSync(cmd *cobra.Command, args []string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	lock, err := model.ReadLockFile(model.DefaultLockPath(projectRoot, config.Dir(), syncGlobal))
	if err != nil {
		return fmt.Errorf("read lock: %w", err)
	}

	gc := git.NewGoGitClient()
	wt := git.NewGoGitWorktree()
	installer := skill.NewInstaller(registry.NewManager(gc), wt, gc)
	reconciler := skill.NewReconciler(installer)

	result, err := reconciler.Reconcile(lock, projectRoot, config.Dir(), skill.ReconcileOptions{
		DryRun:        syncDryRun,
		KeepUntracked: syncKeepUntracked,
	})
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}

	// Refresh AGENTS.md if the user has opted in (file already present). The
	// reconciler may have changed which skills are visible, so the doc cache
	// can otherwise lie until the next manual `qvr docs`.
	if !syncGlobal && !syncDryRun {
		_ = refreshAgentsMDIfPresent(projectRoot, lock.Entries())
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(result)
	}

	for _, name := range result.Installed {
		printer.Success(fmt.Sprintf("Restored %s", name))
	}
	for _, path := range result.SymlinksFixed {
		printer.Info(fmt.Sprintf("Linked %s", path))
	}
	for _, path := range result.Removed {
		printer.Warning(fmt.Sprintf("Removed orphan %s", path))
	}
	for _, skipped := range result.Skipped {
		printer.Info(fmt.Sprintf("Skipped %s", skipped))
	}
	for _, e := range result.Errors {
		printer.Error(e)
	}
	if len(result.Installed)+len(result.SymlinksFixed)+len(result.Removed) == 0 && len(result.Errors) == 0 {
		printer.Success("Already in sync.")
	}
	return nil
}
