package cmd

import (
	"fmt"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/astra-sh/qvr/internal/ops/rawtrace"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var (
	rederiveAgent   string
	rederiveSession string
)

var auditRederiveCmd = &cobra.Command{
	Use:    "rederive",
	Hidden: true, // low-level plumbing — see `qvr audit --help`
	Short:  "Regenerate persisted spans from captured raw traces",
	Long: `Replay the span projection over already-captured raw traces and persist
the result, so the derived views ('qvr audit logs', the UI timeline) reflect the
current deriver. Capture derives spans inline as it tails a transcript; this
backfills sessions captured before their agent had a deriver (or by an older
deriver version). It never re-captures and never touches the raw bytes.

Scope it with --agent and/or --session; with no flags every captured session is
re-derived.`,
	Args: cobra.NoArgs,
	RunE: runAuditRederive,
}

func init() {
	rf := auditRederiveCmd.Flags()
	rf.StringVar(&rederiveAgent, "agent", "", "only re-derive sessions for this agent")
	rf.StringVar(&rederiveSession, "session", "", "only re-derive this canonical session id")
	auditCmd.AddCommand(auditRederiveCmd)
}

// rederiveSummary is the JSON/text report of a rederive run.
type rederiveSummary struct {
	Sessions    int `json:"sessions"` // sessions re-derived
	Spans       int `json:"spans"`    // total spans persisted
	SkippedNoDB int `json:"skipped_no_deriver"`
}

func runAuditRederive(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if !auditDBExists(cfg) {
		if outputFormat == "json" {
			return printer.JSON(rederiveSummary{})
		}
		printer.Info("No traces recorded yet")
		return nil
	}

	s, err := openAuditStore(cmd.Context(), cfg, false)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	sessions, err := targetSessions(cmd, s)
	if err != nil {
		return err
	}

	var sum rederiveSummary
	for _, sess := range sessions {
		// A session whose agent has no deriver can't yield spans; skip it
		// (rather than replacing its spans with an empty set) and report it.
		if _, ok := derive.Get(sess.AgentName); !ok {
			sum.SkippedNoDB++
			continue
		}
		n, _, err := rawtrace.Rederive(cmd.Context(), s, sess.SessionID, sess.AgentName)
		if err != nil {
			printer.Warning(fmt.Sprintf("skip session %s: %v", sess.SessionID, err))
			continue
		}
		sum.Sessions++
		sum.Spans += n
	}

	if outputFormat == "json" {
		return printer.JSON(sum)
	}
	printer.Success(fmt.Sprintf("Re-derived %s, %s",
		output.Plural(sum.Sessions, "session"), output.Plural(sum.Spans, "span")))
	if sum.SkippedNoDB > 0 {
		printer.Info(fmt.Sprintf("%s skipped — no deriver for their agent", output.Plural(sum.SkippedNoDB, "session")))
	}
	return nil
}

// targetSessions resolves the sessions to re-derive from the --agent/--session
// flags. A bare --session is looked up among the listed sessions so its agent
// (needed to pick a deriver) is known.
func targetSessions(cmd *cobra.Command, s store.Store) ([]*store.RawSession, error) {
	sessions, err := s.ListRawSessions(cmd.Context(), &store.RawSessionFilter{Agent: rederiveAgent})
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	if rederiveSession == "" {
		return sessions, nil
	}
	want, err := uuid.Parse(rederiveSession)
	if err != nil {
		return nil, fmt.Errorf("invalid --session: %w", err)
	}
	for _, sess := range sessions {
		if sess.SessionID == want {
			return []*store.RawSession{sess}, nil
		}
	}
	return nil, fmt.Errorf("session %s not found", want)
}
