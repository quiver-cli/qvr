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

var (
	installTargets []string
	installGlobal  bool
	installForce   bool
)

var installCmd = &cobra.Command{
	Use:   "install [skill[@version]]...",
	Short: "Install skills from configured registries",
	Long: `Install one or more skills from configured registries as sparse-checkout worktrees.
When called with no arguments, restores every skill recorded in the lock file.`,
	RunE: runInstall,
}

func init() {
	installCmd.Flags().StringSliceVar(&installTargets, "target", nil,
		"agent target(s) to install into (repeatable). Defaults to default_target (which may itself be comma-separated, e.g. \"claude,cursor\").")
	installCmd.Flags().BoolVar(&installGlobal, "global", false,
		"install into the user-global agent directory")
	installCmd.Flags().BoolVar(&installForce, "force", false,
		"allow replacing an existing lock entry at a different ref (otherwise refused)")
	rootCmd.AddCommand(installCmd)
}

func runInstall(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	targets := installTargets
	if len(targets) == 0 && len(args) > 0 {
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
	mgr := registry.NewManager(gc)
	installer := skill.NewInstaller(mgr, wt, gc)

	if len(args) == 0 {
		results, err := installer.RestoreAll(skill.InstallRequest{
			ProjectRoot: projectRoot,
			Global:      installGlobal,
		})
		if err != nil {
			return fmt.Errorf("restore: %w", err)
		}
		refreshAgentsMDFromLock(projectRoot)
		if printer.Format == output.FormatJSON {
			return printer.JSON(results)
		}
		if len(results) == 0 {
			printer.Info("Nothing to restore — lock file has no skills.")
			return nil
		}
		for _, r := range results {
			printer.Success(fmt.Sprintf("Restored %s@%s → %v", r.Name, r.Version, r.Targets))
		}
		return nil
	}

	var results []*skill.InstallResult
	for _, ref := range args {
		result, err := installer.Install(skill.InstallRequest{
			Skill:       ref,
			Targets:     targets,
			Global:      installGlobal,
			ProjectRoot: projectRoot,
			Force:       installForce,
		})
		if err != nil {
			// Emit any successful installs before propagating the failure so
			// scripted callers can see partial progress. In JSON mode we only
			// emit when there is something to report — `null` would otherwise
			// interleave with the error envelope printed by Execute().
			if len(results) > 0 {
				refreshAgentsMDFromLock(projectRoot)
				if printer.Format == output.FormatJSON {
					_ = printer.JSON(results)
				} else {
					for _, r := range results {
						printer.Success(fmt.Sprintf("Installed %s@%s → %v", r.Name, r.Version, r.Targets))
					}
				}
			}
			return fmt.Errorf("install %s: %w", ref, err)
		}
		results = append(results, result)
	}
	refreshAgentsMDFromLock(projectRoot)
	if printer.Format == output.FormatJSON {
		return printer.JSON(results)
	}
	for _, r := range results {
		printer.Success(fmt.Sprintf("Installed %s@%s → %v", r.Name, r.Version, r.Targets))
	}
	return nil
}
