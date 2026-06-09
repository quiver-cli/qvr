package cmd

import (
	"context"
	"fmt"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/spf13/cobra"
)

var auditEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Turn on the audit pipeline",
	Long: `Enable sets ops.enabled in config and creates the SkillOps database.
Once enabled, installed agent hooks start recording events. Pair with
'qvr audit install-hooks' to wire your agents.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return setAuditEnabled(cmd.Context(), true)
	},
}

var auditDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Turn off the audit pipeline",
	Long: `Disable sets ops.enabled=false. Installed hooks still fire but capture
becomes a silent no-op — no traces are recorded. The database and any installed
hooks are left in place; re-enable with 'qvr audit enable'.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return setAuditEnabled(cmd.Context(), false)
	},
}

func setAuditEnabled(ctx context.Context, enabled bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.Ops.Enabled = enabled
	if err := config.Save(cfg); err != nil {
		return err
	}

	// Eagerly create + migrate the database on enable so the command does what
	// its help advertises ("creates the SkillOps database") instead of deferring
	// schema creation to the first captured event. Opening read-write runs the
	// migrations and leaves a healthy skillops.db on disk; we close it right
	// away since hooks open their own handle per event. (#144)
	if enabled {
		st, derr := store.Open(ctx, store.OpenOptions{Path: ops.DBPath(cfg)})
		if derr != nil {
			return fmt.Errorf("create skillops database: %w", derr)
		}
		if cerr := st.Close(); cerr != nil {
			return fmt.Errorf("close skillops database: %w", cerr)
		}
	}

	state := "disabled"
	if enabled {
		state = "enabled"
	}
	if outputFormat == "json" {
		return printer.JSON(map[string]any{"enabled": enabled, "db_path": ops.DBPath(cfg)})
	}
	printer.Success("Audit pipeline " + state)
	if enabled {
		printer.Detail("database ready at " + ops.DBPath(cfg))
		printer.Hint("run `qvr audit install-hooks` to wire your agents")
	}
	return nil
}

func init() {
	auditCmd.AddCommand(auditEnableCmd)
	auditCmd.AddCommand(auditDisableCmd)
}
