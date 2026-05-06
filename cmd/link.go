package cmd

import (
	"fmt"
	"os"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/registry"
	"github.com/raks097/quiver/internal/skill"
	"github.com/spf13/cobra"
)

// runLink auto-discovers a single skill directory under a registry-style
// `skills/<name>/` layout (same rule `qvr validate` uses) so `qvr link .`
// inside a one-skill registry root Just Works instead of reporting "SKILL.md
// not found". Multi-skill roots still error and list the candidates.

var (
	linkTargets []string
	linkGlobal  bool
)

var linkCmd = &cobra.Command{
	Use:   "link <local-path>",
	Short: "Symlink a local skill directory for development",
	Long:  "Direct symlink from agent dirs to a local path. No worktree, no git — changes are immediate.",
	Args:  cobra.ExactArgs(1),
	RunE:  runLink,
}

func init() {
	linkCmd.Flags().StringSliceVar(&linkTargets, "target", nil, "agent target(s) (repeatable)")
	linkCmd.Flags().BoolVar(&linkGlobal, "global", false, "symlink into the user-global agent dir")
	rootCmd.AddCommand(linkCmd)
}

func runLink(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	targets := linkTargets
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

	resolved, discovered, err := resolveSkillDir(args[0])
	if err != nil {
		if len(discovered) > 1 {
			printer.Error(err.Error())
			for _, d := range discovered {
				fmt.Fprintf(printer.Out, "  %s\n", d)
			}
			return fmt.Errorf("ambiguous skill path")
		}
		return fmt.Errorf("link: %w", err)
	}
	if resolved != args[0] {
		printer.Info(fmt.Sprintf("discovered skill at %s", resolved))
	}

	result, err := installer.Link(resolved, skill.InstallRequest{
		Targets:     targets,
		Global:      linkGlobal,
		ProjectRoot: projectRoot,
	})
	if err != nil {
		return fmt.Errorf("link: %w", err)
	}
	refreshAgentsMDFromLock(projectRoot)
	if printer.Format == output.FormatJSON {
		return printer.JSON(result)
	}
	printer.Success(fmt.Sprintf("Linked %s → %v", result.Name, result.Targets))
	return nil
}
