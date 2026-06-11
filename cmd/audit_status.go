package cmd

import (
	"fmt"
	"slices"
	"time"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/spf13/cobra"
)

var auditStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show per-agent capture status",
	Long: `Reports, for every agent qvr can derive (plus any agent with recorded
data): whether a span deriver exists for it (DERIVES), how many raw rows and
sessions have been recorded, and when the last event for that agent landed.
DERIVES=no means the agent is raw-only — its traces are stored verbatim, but
the derived views (logs, spans, UI timeline) stay empty until a deriver ships.
RECORDED counts individual raw rows (transcript lines); SESSIONS counts the
runs they group into, so RECORDED is normally several times SESSIONS.`,
	Args: cobra.NoArgs,
	RunE: runAuditStatus,
}

func init() {
	auditCmd.AddCommand(auditStatusCmd)
}

type agentStatus struct {
	Agent     string `json:"agent"`
	Derives   bool   `json:"derives"` // a span deriver exists for this agent
	Recorded  int64  `json:"recorded"`
	Sessions  int64  `json:"sessions"`
	LastEvent string `json:"last_event,omitempty"`
}

func runAuditStatus(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Open the store read-only for counters, but only if it exists (a fresh
	// setup has no DB yet).
	var s store.Store
	if auditDBExists(cfg) {
		s, err = openAuditStore(cmd.Context(), cfg, true)
		if err != nil {
			return fmt.Errorf("open audit store: %w", err)
		}
		defer s.Close()
	}

	statuses := make([]agentStatus, 0, 8)
	for _, agent := range statusAgents(cmd, s) {
		statuses = append(statuses, collectAgentStatus(cmd, s, agent))
	}

	if outputFormat == "json" {
		return printer.JSON(map[string]any{"enabled": ops.Enabled(cfg), "agents": statuses})
	}

	// Always surface the global pipeline switch: a disabled pipeline records
	// nothing, and without the banner that looks identical to a healthy idle one.
	if ops.Enabled(cfg) {
		printer.Info("Audit pipeline: enabled")
	} else {
		printer.Info("Audit pipeline: DISABLED — no sessions are recorded")
		printer.Hint("run `qvr audit enable` to start recording")
	}
	headers := []string{"AGENT", "DERIVES", "RECORDED", "SESSIONS", "LAST EVENT"}
	rows := make([][]string, 0, len(statuses))
	for _, as := range statuses {
		rows = append(rows, []string{
			as.Agent,
			yesNo(as.Derives),
			fmt.Sprintf("%d", as.Recorded),
			fmt.Sprintf("%d", as.Sessions),
			orDash(as.LastEvent),
		})
	}
	printer.Table(headers, rows)
	return nil
}

// statusAgents merges the deriver registry with any agents that have recorded
// rows, so legacy / raw-only data stays visible alongside derivable agents.
func statusAgents(cmd *cobra.Command, s store.Store) []string {
	agents := derive.Registered()
	if s != nil {
		if recorded, err := s.DistinctRawAgents(cmd.Context()); err == nil {
			for _, a := range recorded {
				if !slices.Contains(agents, a) {
					agents = append(agents, a)
				}
			}
		}
	}
	slices.Sort(agents)
	return agents
}

// collectAgentStatus assembles the per-agent status row from the deriver
// registry plus the (optional) store's recorded-trace counters.
func collectAgentStatus(cmd *cobra.Command, s store.Store, agent string) agentStatus {
	as := agentStatus{Agent: agent}
	_, as.Derives = derive.Get(agent)
	if s != nil {
		if ts, lErr := s.LatestRawAt(cmd.Context(), agent); lErr == nil && ts != nil {
			as.LastEvent = ts.Local().Format(time.RFC3339)
		}
		if n, cErr := s.CountRawSessions(cmd.Context(), nil, agent); cErr == nil {
			as.Sessions = n
		}
		if n, cErr := s.CountRawTraces(cmd.Context(), nil, agent); cErr == nil {
			as.Recorded = n
		}
	}
	return as
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
