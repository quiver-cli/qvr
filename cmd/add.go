package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/registry"
	"github.com/raks097/quiver/internal/skill"
	"github.com/spf13/cobra"
)

var (
	addTargets []string
	addGlobal  bool
	addForce   bool
	addFrozen  bool
)

var addCmd = &cobra.Command{
	Use:   "add <skill>[@<ref>]...",
	Short: "Add one or more skills from registered sources to the project lock",
	Long: `Add a skill (by name) from any registered source to the current project's
lock file. The skill is resolved against every configured registry; pin a
specific branch, tag, or commit with @<ref>.

  qvr add tdd                       # writes ./qvr.lock, symlinks .claude/skills/tdd
  qvr add tdd@v2                    # pin a branch or tag
  qvr add --global diagnose         # ambient lane: appears in every Claude session
  qvr add tdd lint review           # batch add — each must resolve to a registered skill

Need to add a new source first? Use:

  qvr registry add <url>

The lockfile is the only source of truth for what the agent loads. Anything
under .claude/skills/ that isn't in qvr.lock is hidden on the next ` + "`qvr sync`" + `.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runAdd,
}

func init() {
	addCmd.Flags().StringSliceVar(&addTargets, "target", nil,
		"agent target(s) to install into (repeatable). Defaults to default_target (which may itself be comma-separated, e.g. \"claude,cursor\").")
	addCmd.Flags().BoolVar(&addGlobal, "global", false,
		"write to the user-global lock and symlink under ~/.<agent>/skills/ instead of the project")
	addCmd.Flags().BoolVar(&addForce, "force", false,
		"allow replacing an existing lock entry at a different ref")
	addCmd.Flags().BoolVar(&addFrozen, "frozen", false,
		"refuse drift from the recorded subtree hash; the skill must already be in the lock")
	rootCmd.AddCommand(addCmd)
}

func runAdd(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	targets := addTargets
	if len(targets) == 0 {
		targets = config.ParseDefaultTargets(cfg.DefaultTarget)
		if len(targets) == 0 {
			return fmt.Errorf("no --target specified and default_target is unset")
		}
	}

	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}

	gc := git.NewGoGitClient()
	wt := git.NewGoGitWorktree()
	installer := skill.NewInstaller(registry.NewManager(gc), wt, gc)

	var results []*skill.InstallResult
	var firstErr error
	for _, ref := range args {
		result, err := installer.Install(skill.InstallRequest{
			Skill:       ref,
			Targets:     targets,
			Global:      addGlobal,
			ProjectRoot: projectRoot,
			Force:       addForce,
			Frozen:      addFrozen,
		})
		if err != nil {
			// Skill not found is the headline error — point at `qvr registry add`
			// so the user knows the next step. Everything else falls through with
			// the wrapped error.
			if errors.Is(err, skill.ErrSkillNotFound) {
				err = fmt.Errorf("no registered source contains a skill named %q — register one with `qvr registry add <url>`", ref)
			}
			printer.Error(fmt.Sprintf("add %s: %v", ref, err))
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		results = append(results, result)
	}

	if !addGlobal {
		refreshAgentsMDFromLock(projectRoot)
	}

	if printer.Format == output.FormatJSON {
		if jerr := printer.JSON(results); jerr != nil {
			return jerr
		}
		if firstErr != nil {
			return errJSONHandled
		}
		return nil
	}
	for _, r := range results {
		printer.Success(fmt.Sprintf("Added %s@%s → %v", r.Name, r.Version, r.Targets))
	}
	return firstErr
}
