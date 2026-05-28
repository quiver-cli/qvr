package cmd

import (
	"fmt"
	"os"
	"os/user"
	"strings"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/skill"
	"github.com/spf13/cobra"
)

var (
	editBranch string
	editGlobal bool
)

var editCmd = &cobra.Command{
	Use:   "edit <skill>",
	Short: "Branch a skill off its current ref so local edits don't touch upstream",
	Long: `Create a new local branch on the skill's worktree at the current HEAD and
switch the worktree onto it. Subsequent edits and 'qvr push' will land on the
new branch, keeping shared refs (default branches, release tags) untouched.

The branch name defaults to qvr/<user>/<skill>; override with --branch.`,
	Args: cobra.ExactArgs(1),
	RunE: runEdit,
}

func init() {
	editCmd.Flags().StringVar(&editBranch, "branch", "", "branch name to create (defaults to qvr/<user>/<skill>)")
	editCmd.Flags().BoolVar(&editGlobal, "global", false, "operate on the user-global lock file instead of the project lock")
	rootCmd.AddCommand(editCmd)
}

func runEdit(cmd *cobra.Command, args []string) error {
	name := args[0]
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	lock, err := model.ReadLockFile(model.DefaultLockPath(projectRoot, config.Dir(), editGlobal))
	if err != nil {
		return fmt.Errorf("read lock: %w", err)
	}
	entry, err := lock.Get(name)
	if err != nil {
		return err
	}

	branch := editBranch
	if branch == "" {
		branch = defaultEditBranch(name)
	}
	// Idempotent no-op: matches the behavior of `switch`/`upgrade`/`install`
	// so wrapper scripts can call `qvr edit <skill>` defensively without
	// guarding with `qvr info` first.
	if branch == entry.Ref {
		if printer.Format == output.FormatJSON {
			return printer.JSON(map[string]any{
				"skill":   entry,
				"status":  "already-editing",
				"message": fmt.Sprintf("already editing on %s", branch),
			})
		}
		printer.Info(fmt.Sprintf("%s: already editing on %s", entry.Name, branch))
		return nil
	}

	syncer := skill.NewSyncer(git.NewGoGitWorktree(), git.NewGoGitClient())
	updated, warning, err := syncer.CreateEditBranch(cmd.Context(), entry, branch)
	if err != nil {
		return fmt.Errorf("edit: %w", err)
	}
	if err := skill.ApplySwitch(updated, projectRoot, editGlobal); err != nil {
		return err
	}

	lock.Put(updated)
	if err := lock.Write(); err != nil {
		return fmt.Errorf("write lock: %w", err)
	}
	if !editGlobal {
		_ = refreshAgentsMDIfPresent(projectRoot, lock.Entries())
	}
	if printer.Format == output.FormatJSON {
		out := map[string]any{"skill": updated}
		if warning != "" {
			out["warning"] = warning
		}
		return printer.JSON(out)
	}
	if warning != "" {
		printer.Warning(warning)
	}
	printer.Success(fmt.Sprintf("%s: editing on %s (from %s)", updated.Name, updated.Ref, shortHash(updated.ResolvedSHA)))
	return nil
}

func defaultEditBranch(skillName string) string {
	login := "local"
	if u, err := user.Current(); err == nil && u.Username != "" {
		login = sanitizeLogin(u.Username)
	}
	return fmt.Sprintf("qvr/%s/%s", login, skillName)
}

// sanitizeLogin strips Windows DOMAIN\ prefixes and any characters that would
// confuse a git ref name.
func sanitizeLogin(s string) string {
	if i := strings.LastIndex(s, "\\"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.ToLower(s)
	clean := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			clean = append(clean, r)
		}
	}
	if len(clean) == 0 {
		return "local"
	}
	return string(clean)
}
