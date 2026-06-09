package cmd

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var (
	rawSession string
	rawAgent   string
	rawSource  string
	rawLimit   int
)

var auditRawCmd = &cobra.Command{
	Use:    "raw",
	Hidden: true, // low-level plumbing — see `qvr audit --help`
	Short:  "Print captured traces exactly as the agent produced them",
	Long: `Emit the raw, verbatim trace rows — the agent's own transcript lines and
hook payloads, byte-for-byte, with no parsing or normalization. In text mode the
native JSONL is reproduced line by line (so you get back exactly what the agent
wrote); --output json wraps each row with its capture metadata.`,
	Args: cobra.NoArgs,
	RunE: runAuditRaw,
}

var auditSpansCmd = &cobra.Command{
	Use:    "spans",
	Hidden: true, // low-level plumbing — see `qvr audit --help`
	Short:  "Derive OpenTelemetry spans from captured raw traces",
	Long: `Project the raw traces of a session into OpenTelemetry spans
(Turn / Tool / Skill), using gen_ai.* semantic conventions plus a skill.name
tag. This is a regenerable view over the raw bytes — it never re-captures. Use
--output json for the span list, or --otlp for an OTLP payload ready to POST to
any OTLP consumer.`,
	Args: cobra.NoArgs,
	RunE: runAuditSpans,
}

var spansOTLP bool

func init() {
	rf := auditRawCmd.Flags()
	rf.StringVar(&rawSession, "session", "", "filter by canonical session id")
	rf.StringVar(&rawAgent, "agent", "", "filter by agent name")
	rf.StringVar(&rawSource, "source", "", "filter by source (transcript | hook_payload)")
	rf.IntVar(&rawLimit, "limit", 0, "maximum rows (0 = no limit)")
	auditCmd.AddCommand(auditRawCmd)

	sf := auditSpansCmd.Flags()
	sf.StringVar(&rawSession, "session", "", "session id to derive (required unless --agent)")
	sf.StringVar(&rawAgent, "agent", "", "agent name (derive every captured session for this agent)")
	sf.BoolVar(&spansOTLP, "otlp", false, "emit an OTLP resourceSpans payload")
	auditCmd.AddCommand(auditSpansCmd)
}

func rawFilter() (*store.RawTraceFilter, error) {
	f := &store.RawTraceFilter{Limit: rawLimit}
	if rawAgent != "" {
		f.Agents = []string{rawAgent}
	}
	if rawSource != "" {
		f.Sources = []string{rawSource}
	}
	if rawSession != "" {
		id, err := uuid.Parse(rawSession)
		if err != nil {
			return nil, fmt.Errorf("invalid --session id %q: %w", rawSession, err)
		}
		f.SessionID = &id
	}
	return f, nil
}

func runAuditRaw(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	filter, err := rawFilter()
	if err != nil {
		return err
	}
	if !auditDBExists(cfg) {
		if outputFormat == "json" {
			return printer.JSON([]any{})
		}
		printer.Info("No traces recorded yet")
		return nil
	}
	s, err := openAuditStore(cmd.Context(), cfg, true)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	rows, err := s.QueryRawTraces(cmd.Context(), filter)
	if err != nil {
		return fmt.Errorf("query raw traces: %w", err)
	}

	if outputFormat == "json" {
		out := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, map[string]any{
				"agent_name":  r.AgentName,
				"session_id":  r.SessionID.String(),
				"seq":         r.Seq,
				"source":      r.Source,
				"hook_type":   r.HookType,
				"source_path": r.SourcePath,
				"captured_at": r.CapturedAt,
				"raw":         json.RawMessage(r.Raw), // emit native JSON inline
			})
		}
		return printer.JSON(out)
	}

	// Text mode: reproduce the verbatim native bytes, one row per line.
	w := cmd.OutOrStdout()
	for _, r := range rows {
		fmt.Fprintln(w, string(r.Raw))
	}
	if len(rows) == 0 {
		printer.Info("No traces match")
	}
	return nil
}

func runAuditSpans(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	filter, err := rawFilter()
	if err != nil {
		return err
	}
	if filter.SessionID == nil && rawAgent == "" {
		return fmt.Errorf("specify --session <id> or --agent <name>")
	}
	// Spans are derived per session from transcript rows only.
	filter.Sources = []string{"transcript"}

	if !auditDBExists(cfg) {
		if outputFormat == "json" {
			return printer.JSON([]derive.Span{})
		}
		printer.Info("No traces recorded yet")
		return nil
	}
	s, err := openAuditStore(cmd.Context(), cfg, true)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	rows, err := s.QueryRawTraces(cmd.Context(), filter)
	if err != nil {
		return fmt.Errorf("query raw traces: %w", err)
	}

	spans, err := deriveGrouped(rows)
	if err != nil {
		return err
	}

	if spansOTLP {
		return printer.JSON(derive.ToOTLP(spans))
	}
	if outputFormat == "json" {
		return printer.JSON(spans)
	}
	if len(spans) == 0 {
		// Distinguish "nothing was captured" from "rows exist but didn't
		// derive". The latter is usually an agent with no registered deriver
		// (deriveGrouped already warned per session with the real cause) — say
		// so, rather than the false "no transcript captured" when rows exist.
		if len(rows) > 0 {
			printer.Info("No spans derived for this scope — captured transcript rows did not yield spans")
		} else {
			printer.Info("No spans derived — no transcript captured for this scope")
		}
		return nil
	}
	headers := []string{"KIND", "NAME", "MODEL", "TOKENS", "SKILL"}
	tableRows := make([][]string, 0, len(spans))
	for _, sp := range spans {
		tableRows = append(tableRows, []string{
			sp.Kind,
			sp.Name,
			attrString(sp.Attributes, "gen_ai.request.model"),
			attrTokens(sp.Attributes),
			attrString(sp.Attributes, "skill.name"),
		})
	}
	printer.Table(headers, tableRows)
	return nil
}

// deriveGrouped derives spans per session. Rows arrive ordered by
// (session_id, seq), so a session is a contiguous run; we derive each run
// independently and concatenate. A session whose agent has no registered
// deriver is skipped (not fatal — other sessions still derive).
func deriveGrouped(rows []*ops.RawTrace) ([]derive.Span, error) {
	var spans []derive.Span
	var batch []*ops.RawTrace
	var cur uuid.UUID
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		s, err := derive.DeriveSession(batch)
		if err != nil {
			// Non-fatal by design: a session whose agent has no registered
			// deriver (or otherwise fails) is skipped so the remaining
			// sessions still derive. Surface it as a warning rather than
			// dropping it silently, so a failed derivation is visible.
			printer.Warning(fmt.Sprintf("skip session %s: %v", cur, err))
		} else {
			// Promote skill identity from qvr.lock so name collisions across
			// registries/versions stay distinguishable in the output (#146).
			derive.EnrichSkillIdentity(s, batch)
			spans = append(spans, s...)
		}
		batch = nil
		return nil
	}
	for _, r := range rows {
		if r.SessionID != cur && len(batch) > 0 {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		cur = r.SessionID
		batch = append(batch, r)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return spans, nil
}

func attrString(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func attrTokens(m map[string]any) string {
	in, _ := m["gen_ai.usage.input_tokens"].(int)
	out, _ := m["gen_ai.usage.output_tokens"].(int)
	if in+out > 0 {
		return strconv.Itoa(in + out)
	}
	return ""
}
