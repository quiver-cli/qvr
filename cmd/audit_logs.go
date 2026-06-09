package cmd

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var (
	logsAgent   string
	logsKind    string
	logsSession string
	logsLimit   int
)

var auditLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Show recent derived activity (spans)",
	Long: `Query the derived span view — the Turn / Tool / Skill spans projected
from captured raw traces. Filter by agent, span kind, or session. For the
verbatim native trace use 'qvr audit raw'; for a session's full span tree or an
OTLP payload use 'qvr audit spans'.`,
	Args: cobra.NoArgs,
	RunE: runAuditLogs,
}

func init() {
	f := auditLogsCmd.Flags()
	f.StringVar(&logsAgent, "agent", "", "filter by agent name")
	f.StringVar(&logsKind, "kind", "", "filter by span kind (LLM, TOOL, SKILL)")
	f.StringVar(&logsSession, "session", "", "filter by session id")
	f.IntVar(&logsLimit, "limit", 50, "maximum spans to show (0 = no limit)")
	auditCmd.AddCommand(auditLogsCmd)
}

func runAuditLogs(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	f := &store.SpanFilter{Limit: logsLimit}
	if logsAgent != "" {
		f.Agents = []string{logsAgent}
	}
	if logsKind != "" {
		f.Kinds = []string{strings.ToUpper(logsKind)}
	}
	if logsSession != "" {
		id, perr := uuid.Parse(logsSession)
		if perr != nil {
			return fmt.Errorf("invalid --session id %q: %w", logsSession, perr)
		}
		f.SessionID = &id
	}

	if !auditDBExists(cfg) {
		if outputFormat == "json" {
			return printer.JSON([]*store.SpanRow{})
		}
		printer.Info("no activity recorded yet")
		return nil
	}

	s, err := openAuditStore(cmd.Context(), cfg, true)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	spans, err := s.QuerySpans(cmd.Context(), f)
	if err != nil {
		return fmt.Errorf("query spans: %w", err)
	}

	if outputFormat == "json" {
		if spans == nil {
			spans = []*store.SpanRow{}
		}
		return printer.JSON(spans)
	}
	if len(spans) == 0 {
		printer.Info("no activity matches")
		return nil
	}
	headers := []string{"TIME", "AGENT", "KIND", "NAME", "SKILL"}
	rows := make([][]string, 0, len(spans))
	for _, sp := range spans {
		rows = append(rows, []string{
			msTime(sp.StartMs),
			sp.AgentName,
			sp.Kind,
			truncTarget(sp.Name),
			spanAttr(sp.Attributes, "skill.name"),
		})
	}
	printer.Table(headers, rows)
	return nil
}

// msTime renders an epoch-ms span start as a local short timestamp.
func msTime(ms int64) string {
	if ms == 0 {
		return "-"
	}
	return time.UnixMilli(ms).Local().Format("01-02 15:04:05")
}

// spanAttr pulls a string attribute out of a span's JSON attributes blob.
func spanAttr(attrsJSON, key string) string {
	if attrsJSON == "" {
		return ""
	}
	// Cheap substring extraction avoids a full unmarshal for one field.
	needle := `"` + key + `":"`
	_, after, ok := strings.Cut(attrsJSON, needle)
	if !ok {
		return ""
	}
	rest := after
	if before, _, ok := strings.Cut(rest, "\""); ok {
		return before
	}
	return ""
}

// parseTimeFlag accepts either an RFC3339 timestamp or a relative duration like
// "1d", "12h", "30m" (interpreted as "ago").
func parseTimeFlag(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	dur, err := parseRelative(s)
	if err != nil {
		return time.Time{}, err
	}
	return time.Now().UTC().Add(-dur), nil
}

// parseRelative parses a duration, additionally supporting a trailing "d" for
// whole days (e.g. "7d").
func parseRelative(s string) (time.Duration, error) {
	if rest, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(rest)
		if err != nil {
			return 0, fmt.Errorf("bad day count %q", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// truncTarget clips a label for table display.
func truncTarget(s string) string {
	const max = 50
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
