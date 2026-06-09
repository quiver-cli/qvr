package cmd

import (
	"fmt"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/spf13/cobra"
)

var (
	installAgent  string
	installDryRun bool
	installForce  bool
)

var auditInstallCmd = &cobra.Command{
	Use:   "install-hooks",
	Short: "Wire qvr into your agents' native hooks",
	Long: `Detects installed agents and adds a hook that pipes each tool, file,
and session event into 'qvr _hook'. The original agent config is backed up
under $QUIVER_HOME/backups/<agent>/<timestamp>/ first. Re-running is a no-op
unless --force is given. With no --agent, every detected agent is wired.`,
	Args: cobra.NoArgs,
	RunE: runAuditInstall,
}

func init() {
	auditInstallCmd.Flags().StringVar(&installAgent, "agent", "", "install for a single agent (default: all detected)")
	auditInstallCmd.Flags().BoolVar(&installDryRun, "dry-run", false, "print planned changes without writing")
	auditInstallCmd.Flags().BoolVar(&installForce, "force", false, "reinstall even if hooks are already present")
	auditCmd.AddCommand(auditInstallCmd)
}

// installOutcome is the per-agent result rendered by install-hooks.
type installOutcome struct {
	Agent      string   `json:"agent"`
	Detected   bool     `json:"detected"`
	Installed  bool     `json:"installed"`
	HooksAdded []string `json:"hooks_added,omitempty"`
	BackupPath string   `json:"backup_path,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
	Error      string   `json:"error,omitempty"`
}

func runAuditInstall(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	installers, err := selectInstallers(installAgent)
	if err != nil {
		return err
	}

	// Open the store once (unless dry-run) to record self-audit rows.
	var s store.Store
	if !installDryRun {
		s, err = openAuditStore(cmd.Context(), cfg, false)
		if err != nil {
			return fmt.Errorf("open audit store: %w", err)
		}
		defer s.Close()
	}

	outcomes := make([]installOutcome, 0, len(installers))
	for _, inst := range installers {
		out := installOutcome{Agent: inst.Name()}

		det, derr := inst.Detect()
		if derr != nil {
			out.Error = derr.Error()
			outcomes = append(outcomes, out)
			continue
		}
		out.Detected = det.Detected
		if !det.Detected {
			out.Warnings = append(out.Warnings, det.Message)
			outcomes = append(outcomes, out)
			continue
		}

		res, ierr := inst.Install(ops.InstallOptions{DryRun: installDryRun, Force: installForce})
		out.HooksAdded = res.HooksAdded
		out.BackupPath = res.BackupPath
		out.Warnings = append(out.Warnings, res.Warnings...)
		if ierr != nil {
			out.Error = ierr.Error()
		} else {
			out.Installed = len(res.HooksAdded) > 0
		}

		if s != nil {
			result := store.ResultAudit_Success
			errMsg := ""
			if ierr != nil {
				result = store.ResultAudit_Error
				errMsg = ierr.Error()
			}
			if aerr := recordSelfAudit(cmd.Context(), s, store.ActionAdapterInstall, inst.Name(), result, errMsg, map[string]any{
				"hooks":       res.HooksAdded,
				"backup_path": res.BackupPath,
				"dry_run":     installDryRun,
			}); aerr != nil {
				out.Warnings = append(out.Warnings, "self-audit write failed: "+aerr.Error())
			}
		}

		outcomes = append(outcomes, out)
	}

	return renderInstallOutcomes(cfg, outcomes)
}

func renderInstallOutcomes(cfg *config.Config, outcomes []installOutcome) error {
	if outputFormat == "json" {
		return printer.JSON(map[string]any{"dry_run": installDryRun, "agents": outcomes})
	}

	anyErr := false
	dim := printer.StyleOut().Dim
	for _, o := range outcomes {
		switch {
		case o.Error != "":
			anyErr = true
			printer.Error(fmt.Sprintf("%s: %s", o.Agent, o.Error))
		case !o.Detected:
			printer.Info(dim(fmt.Sprintf("- %s: not detected", o.Agent)))
		case installDryRun:
			printer.Info(dim(fmt.Sprintf("~ %s: would install %s", o.Agent, output.Plural(len(o.HooksAdded), "hook"))))
		case o.Installed:
			printer.Success(fmt.Sprintf("%s: installed %s", o.Agent, output.Plural(len(o.HooksAdded), "hook")))
			if o.BackupPath != "" {
				printer.Detail("backup: " + o.BackupPath)
			}
		default:
			printer.Info(dim(fmt.Sprintf("- %s: already installed", o.Agent)))
		}
		for _, w := range o.Warnings {
			printer.Warning(fmt.Sprintf("%s: %s", o.Agent, w))
		}
	}

	if !installDryRun && !ops.Enabled(cfg) {
		printer.Warning("audit pipeline is disabled — no events will be recorded")
		printer.Hint("run `qvr audit enable` to start recording")
	}
	if anyErr {
		return errTextHandled
	}
	return nil
}

// selectInstallers resolves the --agent flag to the set of installers to
// operate on. Empty agent → every registered installer.
func selectInstallers(agent string) ([]ops.HookInstaller, error) {
	if agent == "" {
		installers := ops.ListInstallers()
		if len(installers) == 0 {
			return nil, fmt.Errorf("no installable agents are registered")
		}
		return installers, nil
	}
	inst, ok := ops.GetInstaller(agent)
	if !ok {
		return nil, fmt.Errorf("unknown or non-installable agent %q", agent)
	}
	return []ops.HookInstaller{inst}, nil
}
