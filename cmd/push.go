package cmd

import (
	"errors"
	"fmt"
	"os"
	"sort"

	gogit "github.com/go-git/go-git/v5"
	"github.com/spf13/cobra"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/registry"
	"github.com/raks097/quiver/internal/skill"
)

var (
	pushMessage string
	pushAuthor  string
	pushEmail   string
	pushGlobal  bool
	pushDryRun  bool
)

var pushCmd = &cobra.Command{
	Use:   "push <skill>",
	Short: "Commit and push local changes upstream",
	Long: `Stage all changes in the worktree, commit with --message, and push to origin.
Nothing is pushed when the worktree is clean.

Pass --dry-run to print the planned commit (message, author, target branch, file
list) without committing or pushing. The lock file is not modified. Mirrors
the --dry-run convention on qvr sync / cache prune / lock upgrade — every other
state-mutating qvr command offers one, and qvr push is the only one that touches
an upstream registry (issue #67).`,
	Args: cobra.ExactArgs(1),
	RunE: runPush,
}

func init() {
	pushCmd.Flags().StringVarP(&pushMessage, "message", "m", "", "commit message")
	pushCmd.Flags().StringVar(&pushAuthor, "author", "", "commit author name")
	pushCmd.Flags().StringVar(&pushEmail, "email", "", "commit author email")
	pushCmd.Flags().BoolVar(&pushGlobal, "global", false, "read the user-global lock file instead of the project lock")
	pushCmd.Flags().BoolVar(&pushDryRun, "dry-run", false, "print the planned commit without committing or pushing")
	rootCmd.AddCommand(pushCmd)
}

// pushPlan is the JSON-stable preview emitted by `qvr push --dry-run`.
// Mirrors the fields a real push consumes (message, author, target branch,
// the staged + unstaged file list) so a caller can diff this against what
// the live push would do.
type pushPlan struct {
	Name      string   `json:"name"`
	Skill     string   `json:"skill"`
	Worktree  string   `json:"worktree"`
	Branch    string   `json:"branch"`
	Message   string   `json:"message"`
	Author    string   `json:"author"`
	Email     string   `json:"email"`
	NoChanges bool     `json:"no_changes"`
	Files     []string `json:"files"`
}

func runPush(cmd *cobra.Command, args []string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), pushGlobal)

	if pushDryRun {
		return runPushDryRun(args[0], lockPath)
	}

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

// runPushDryRun composes a pushPlan from the worktree's current state and
// emits it without touching the lock or invoking the syncer's Push. Issue
// #67 — gives the user a preview of every mutation a real push would make
// so they can sanity-check before the upstream gets touched.
//
// We deliberately do NOT take the WithLock window: the dry-run is read-only
// against the worktree and reads the lock for entry lookup only; nothing
// is written.
func runPushDryRun(name, lockPath string) error {
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return fmt.Errorf("read lock: %w", err)
	}
	entry, err := lock.Get(name)
	if err != nil {
		return fmt.Errorf("lookup %s: %w", name, err)
	}
	if entry.IsLink() {
		return fmt.Errorf("cannot push a link install — edit the source directly")
	}

	plan := pushPlan{
		Name:     entry.Name,
		Skill:    entry.Name,
		Worktree: skill.EntryWorktreePath(entry),
		Branch:   entry.Ref,
		Message:  pushMessage,
		Author:   pushAuthor,
		Email:    pushEmail,
	}

	repo, err := gogit.PlainOpen(plan.Worktree)
	if err != nil {
		return fmt.Errorf("open worktree: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("worktree handle: %w", err)
	}
	status, err := wt.Status()
	if err != nil {
		return fmt.Errorf("status: %w", err)
	}
	if status.IsClean() {
		plan.NoChanges = true
	} else {
		plan.Files = make([]string, 0, len(status))
		for path := range status {
			plan.Files = append(plan.Files, path)
		}
		sort.Strings(plan.Files)
	}

	// Fallback to repo HEAD branch when the entry doesn't carry one
	// (legacy entries pre-dating ref tracking, or detached-HEAD edits).
	if plan.Branch == "" {
		if head, err := repo.Head(); err == nil && head.Name().IsBranch() {
			plan.Branch = head.Name().Short()
		}
	}

	if printer.Format == output.FormatJSON {
		payload := map[string]any{
			"dry_run": true,
			"planned": plan,
		}
		return printer.JSON(payload)
	}
	if plan.NoChanges {
		printer.Info(fmt.Sprintf("%s: dry-run — no local changes, nothing would be pushed", plan.Name))
		return nil
	}
	printer.Info(fmt.Sprintf("%s: dry-run — would push to %s", plan.Name, plan.Branch))
	if plan.Message != "" {
		fmt.Fprintf(printer.Out, "  message: %s\n", plan.Message)
	}
	if plan.Author != "" || plan.Email != "" {
		fmt.Fprintf(printer.Out, "  author:  %s <%s>\n", plan.Author, plan.Email)
	}
	fmt.Fprintf(printer.Out, "  files:   %d staged/dirty\n", len(plan.Files))
	for _, f := range plan.Files {
		fmt.Fprintf(printer.Out, "    %s\n", f)
	}
	return nil
}
