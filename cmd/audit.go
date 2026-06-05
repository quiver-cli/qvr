package cmd

import (
	"context"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/quiver-cli/qvr/internal/config"
	"github.com/quiver-cli/qvr/internal/ops"
	"github.com/quiver-cli/qvr/internal/ops/store"
	"github.com/spf13/cobra"

	// Side-effect imports: register the per-agent hook installers so the
	// install/status tooling can see them.
	_ "github.com/quiver-cli/qvr/internal/ops/claudecode"
	_ "github.com/quiver-cli/qvr/internal/ops/codex"
	_ "github.com/quiver-cli/qvr/internal/ops/copilot"
	_ "github.com/quiver-cli/qvr/internal/ops/cursor"
	_ "github.com/quiver-cli/qvr/internal/ops/opencode"
)

// auditCmd is the parent for the SkillOps audit-trail surface: enabling
// capture, wiring agent hooks, and querying the recorded events.
var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "[experimental] Record and query skill-attributed agent activity",
	Long: `[EXPERIMENTAL] The audit subsystem is opt-in and its command surface,
storage format, and output shapes may change without notice. It is disabled by
default; nothing is captured until you run 'qvr audit enable' and wire an
agent's hooks with 'qvr audit install-hooks'.

Audit captures an atomic trace of every tool, file, and command an agent runs
— attributed to the skill that was active — into a local SQLite database. Once
enabled, query it with 'qvr audit logs' / 'qvr audit sessions'.

The everyday surface is enable/disable, install-hooks/uninstall-hooks, status,
logs, sessions, and export. The remaining subcommands (ingest, raw, spans,
rederive, gc) are low-level plumbing the hooks and maintenance paths use and
are hidden from this list.`,
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
	printer.Info("nothing recorded yet")
	return nil
}

// recordSelfAudit best-effort writes an install/uninstall row. Errors are
// returned so callers can warn, but they should not fail the command on a
// self-audit write failure.
func recordSelfAudit(ctx context.Context, s store.Store, action, actor, result, errMsg string, details map[string]any) error {
	return s.AppendSelfAudit(ctx, &store.SelfAudit{
		ID:        uuid.New(),
		Timestamp: time.Now().UTC(),
		Action:    action,
		Actor:     actor,
		Result:    result,
		ErrorMsg:  errMsg,
		Details:   details,
	})
}
