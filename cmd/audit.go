package cmd

import (
	"context"
	"os"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/spf13/cobra"
)

// auditCmd is the parent for the SkillOps audit-trail surface: enabling
// capture, importing agent session stores, and querying the recorded events.
var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "[experimental] Record and query skill-attributed agent activity",
	Long: `[EXPERIMENTAL] The audit subsystem is opt-in and its command surface,
storage format, and output shapes may change without notice. It is disabled by
default; nothing is recorded until you run 'qvr audit enable'.

Agents already persist their own session transcripts on disk; audit reads
those native stores directly — no agent configuration is touched. Run
'qvr audit discover' to scan them (months of existing history back-fill
instantly). Each session's verbatim trace lands in a local SQLite database,
attributed to the exact locked skill version that ran. Query it with
'qvr audit logs' / 'qvr audit sessions'.

The everyday surface is enable/disable, discover, status, sessions, logs, and
export. The remaining subcommands (ingest, raw, spans, rederive, gc) are
low-level plumbing the maintenance paths use and are hidden from this list.`,
	// Reject a typo'd subcommand (`qvr audit enabel`) with a non-zero exit
	// instead of silently printing help (issue #169 — the #120 fix missed this
	// parent). No args still prints help.
	RunE: rejectUnknownSubcommand,
}

func init() {
	rootCmd.AddCommand(auditCmd)
}

// openAuditStore opens the SkillOps store at the configured path. readOnly
// callers (logs/sessions/export) pass true so they never create the DB.
func openAuditStore(ctx context.Context, cfg *config.Config, readOnly bool) (store.Store, error) {
	return store.Open(ctx, store.OpenOptions{Path: ops.DBPath(cfg), ReadOnly: readOnly})
}

// auditDBExists reports whether the SkillOps database file is present. The
// read commands short-circuit to an empty result when it isn't, rather than
// failing a read-only open (which skips migrations and would error on the
// missing audit_events table).
func auditDBExists(cfg *config.Config) bool {
	info, err := os.Stat(ops.DBPath(cfg))
	return err == nil && !info.IsDir()
}

// renderEmptyEvents prints the no-results result in the active format.
func renderEmptyEvents() error {
	if outputFormat == "json" {
		return printer.JSON([]any{})
	}
	printer.Info("Nothing recorded yet")
	return nil
}
