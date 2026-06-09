package cmd

import (
	"fmt"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var (
	sessionsSince string
	sessionsAgent string
	sessionsLimit int
)

var auditSessionsCmd = &cobra.Command{
	Use:   "sessions [show <id>]",
	Short: "List recorded agent sessions",
	Long: `Lists agent sessions newest-first with per-session row counts derived
from captured raw traces. Use 'qvr audit sessions show <id>' to print one
session's verbatim raw lines (equivalent to 'qvr audit raw --session <id>').`,
	Args: cobra.ArbitraryArgs,
	RunE: runAuditSessions,
}

func init() {
	auditSessionsCmd.Flags().StringVar(&sessionsSince, "since", "", "only sessions started since this time (e.g. 7d, 24h, or RFC3339)")
	auditSessionsCmd.Flags().StringVar(&sessionsAgent, "agent", "", "filter by agent name (e.g. claude-code, codex)")
	auditSessionsCmd.Flags().IntVar(&sessionsLimit, "limit", 50, "maximum sessions to show (0 = no limit)")
	auditCmd.AddCommand(auditSessionsCmd)
}

func runAuditSessions(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if len(args) >= 1 && args[0] == "show" {
		if len(args) < 2 {
			return fmt.Errorf("usage: qvr audit sessions show <id>")
		}
		return showSession(cmd, cfg, args[1])
	}
	if len(args) > 0 {
		return fmt.Errorf("unknown argument %q (did you mean 'sessions show <id>'?)", args[0])
	}

	if !auditDBExists(cfg) {
		if outputFormat == "json" {
			return printer.JSON([]any{})
		}
		printer.Info("no sessions recorded yet")
		return nil
	}

	s, err := openAuditStore(cmd.Context(), cfg, true)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	f := &store.RawSessionFilter{Agent: sessionsAgent, Limit: sessionsLimit}
	if sessionsSince != "" {
		t, perr := parseTimeFlag(sessionsSince)
		if perr != nil {
			return fmt.Errorf("invalid --since: %w", perr)
		}
		f.Since = &t
	}

	sessions, err := s.ListRawSessions(cmd.Context(), f)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}

	return renderSessions(sessions)
}

// renderSessions emits the session list as JSON or a newest-first table.
func renderSessions(sessions []*store.RawSession) error {
	if outputFormat == "json" {
		if len(sessions) == 0 {
			return printer.JSON([]any{})
		}
		return printer.JSON(sessions)
	}
	if len(sessions) == 0 {
		printer.Info("no sessions recorded yet")
		return nil
	}
	headers := []string{"STARTED", "AGENT", "LINES", "HOOKS", "ROWS", "SESSION ID"}
	rows := make([][]string, 0, len(sessions))
	for _, sess := range sessions {
		rows = append(rows, []string{
			sess.StartedAt.Local().Format("01-02 15:04"),
			sess.AgentName,
			fmt.Sprintf("%d", sess.TranscriptLines),
			fmt.Sprintf("%d", sess.HookPayloads),
			fmt.Sprintf("%d", sess.TotalRows),
			sess.SessionID.String(),
		})
	}
	printer.Table(headers, rows)
	return nil
}

// showSession prints one session's verbatim raw lines.
func showSession(cmd *cobra.Command, cfg *config.Config, idArg string) error {
	id, err := uuid.Parse(idArg)
	if err != nil {
		return fmt.Errorf("invalid session id %q: %w", idArg, err)
	}
	if !auditDBExists(cfg) {
		return renderEmptyEvents()
	}
	s, err := openAuditStore(cmd.Context(), cfg, true)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	rows, err := s.QueryRawTraces(cmd.Context(), &store.RawTraceFilter{SessionID: &id})
	if err != nil {
		return fmt.Errorf("get session traces: %w", err)
	}
	if outputFormat == "json" {
		return printer.JSON(rows)
	}
	if len(rows) == 0 {
		printer.Info("no traces for that session")
		return nil
	}
	w := cmd.OutOrStdout()
	for _, r := range rows {
		fmt.Fprintln(w, string(r.Raw))
	}
	return nil
}
