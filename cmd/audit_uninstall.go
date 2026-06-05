package cmd

import (
	"fmt"

	"github.com/quiver-cli/qvr/internal/config"
	"github.com/quiver-cli/qvr/internal/ops"
	"github.com/quiver-cli/qvr/internal/ops/store"
	"github.com/spf13/cobra"
)

var (
	uninstallAgent  string
	uninstallDryRun bool
	uninstallForce  bool
)

var auditUninstallCmd = &cobra.Command{
	Use:   "uninstall-hooks",
	Short: "Remove qvr's hooks from your agents",
	Long: `Restores each agent's config from the newest Quiver backup, or
surgically strips Quiver's hook entries if no backup exists. With no --agent,
every detected agent is cleaned.`,
	Args: cobra.NoArgs,
	RunE: runAuditUninstall,
}

func init() {
	auditUninstallCmd.Flags().StringVar(&uninstallAgent, "agent", "", "uninstall for a single agent (default: all detected)")
	auditUninstallCmd.Flags().BoolVar(&uninstallDryRun, "dry-run", false, "print planned changes without writing")
	auditUninstallCmd.Flags().BoolVar(&uninstallForce, "force", false, "proceed even if the agent config diverged from Quiver's")
	auditCmd.AddCommand(auditUninstallCmd)
}

type uninstallOutcome struct {
	Agent        string   `json:"agent"`
	Restored     bool     `json:"restored"`
	HooksRemoved []string `json:"hooks_removed,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
	Error        string   `json:"error,omitempty"`
}

func runAuditUninstall(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	installers, err := selectInstallers(uninstallAgent)
	if err != nil {
		return err
	}

	var s store.Store
	if !uninstallDryRun {
		s, err = openAuditStore(cmd.Context(), cfg, false)
		if err != nil {
			return fmt.Errorf("open audit store: %w", err)
		}
		defer s.Close()
	}

	outcomes := make([]uninstallOutcome, 0, len(installers))
	for _, inst := range installers {
		out := uninstallOutcome{Agent: inst.Name()}

		res, uerr := inst.Uninstall(ops.UninstallOptions{DryRun: uninstallDryRun, Force: uninstallForce})
		out.Restored = res.Restored
		out.HooksRemoved = res.HooksRemoved
		out.Warnings = append(out.Warnings, res.Warnings...)
		if uerr != nil {
			out.Error = uerr.Error()
		}

		if s != nil {
			result := store.ResultAudit_Success
			errMsg := ""
			if uerr != nil {
				result = store.ResultAudit_Error
				errMsg = uerr.Error()
			}
			if aerr := recordSelfAudit(cmd.Context(), s, store.ActionAdapterUninstall, inst.Name(), result, errMsg, map[string]any{
				"hooks_removed": res.HooksRemoved,
				"restored":      res.Restored,
				"dry_run":       uninstallDryRun,
			}); aerr != nil {
				out.Warnings = append(out.Warnings, "self-audit write failed: "+aerr.Error())
			}
		}

		outcomes = append(outcomes, out)
	}

	if outputFormat == "json" {
		return printer.JSON(map[string]any{"dry_run": uninstallDryRun, "agents": outcomes})
	}
	anyErr := false
	for _, o := range outcomes {
		switch {
		case o.Error != "":
			anyErr = true
			printer.Error(fmt.Sprintf("%s: %s", o.Agent, o.Error))
		case uninstallDryRun:
			printer.Info(fmt.Sprintf("~ %s: would remove %d hooks", o.Agent, len(o.HooksRemoved)))
		case o.Restored:
			printer.Success(fmt.Sprintf("%s: restored from backup", o.Agent))
		case len(o.HooksRemoved) > 0:
			printer.Success(fmt.Sprintf("%s: removed %d hooks", o.Agent, len(o.HooksRemoved)))
		default:
			printer.Info(fmt.Sprintf("- %s: nothing to remove", o.Agent))
		}
		for _, w := range o.Warnings {
			printer.Warning(fmt.Sprintf("%s: %s", o.Agent, w))
		}
	}
	if anyErr {
		return errTextHandled
	}
	return nil
}
