package cmd

import (
	"fmt"
	"time"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/spf13/cobra"
)

var auditStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show per-agent hook + capture status",
	Long: `Reports, for every installable agent: whether it's detected on this
machine, whether Quiver's hooks are installed and valid, whether a span deriver
exists for it (DERIVES), how many events and sessions have been recorded, how
many hook errors were logged, and when the last event for that agent was
recorded. DERIVES=no means the agent is raw-only — hooks fire and traces land,
but the derived views (logs, spans, UI timeline) stay empty. RECORDED counts
individual events
(tool calls, file ops, …); SESSIONS counts the runs they group into, so
RECORDED is normally several times SESSIONS. ERRORS counts hook parse/ingest
failures — a non-zero value means events are reaching qvr but failing to
record.`,
	Args: cobra.NoArgs,
	RunE: runAuditStatus,
}

func init() {
	auditCmd.AddCommand(auditStatusCmd)
}

type agentStatus struct {
	Agent       string   `json:"agent"`
	DisplayName string   `json:"display_name"`
	Detected    bool     `json:"detected"`
	ConfigPath  string   `json:"config_path,omitempty"`
	Version     string   `json:"version,omitempty"`
	Installed   bool     `json:"installed"`
	Valid       bool     `json:"valid"`
	Derives     bool     `json:"derives"` // a span deriver exists for this agent
	Recorded    int64    `json:"recorded"`
	Sessions    int64    `json:"sessions"`
	Errors      int64    `json:"errors"`
	LastEvent   string   `json:"last_event,omitempty"`
	Issues      []string `json:"issues,omitempty"`
}

func runAuditStatus(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Open the store read-only for last-event lookups, but only if it
	// exists (a fresh setup has no DB yet).
	var s store.Store
	if auditDBExists(cfg) {
		s, err = openAuditStore(cmd.Context(), cfg, true)
		if err != nil {
			return fmt.Errorf("open audit store: %w", err)
		}
		defer s.Close()
	}

	installers := ops.ListInstallers()
	statuses := make([]agentStatus, 0, len(installers))
	for _, inst := range installers {
		statuses = append(statuses, collectAgentStatus(cmd, s, inst))
	}

	if outputFormat == "json" {
		return printer.JSON(map[string]any{"enabled": ops.Enabled(cfg), "agents": statuses})
	}

	// Always surface the global pipeline switch. Without this, a disabled
	// pipeline looked identical to a healthy one — every agent still showed
	// INSTALLED=yes/VALID=yes (the hooks are wired) while `qvr _hook` silently
	// no-op'd, so a user had no way to tell capture was globally off.
	if ops.Enabled(cfg) {
		printer.Info("Audit pipeline: enabled")
	} else {
		printer.Info("Audit pipeline: DISABLED — hooks are wired but no events are recorded")
		printer.Warning("run 'qvr audit enable' to start recording")
	}
	headers := []string{"AGENT", "DETECTED", "INSTALLED", "VALID", "DERIVES", "RECORDED", "SESSIONS", "ERRORS", "LAST EVENT"}
	rows := make([][]string, 0, len(statuses))
	for _, as := range statuses {
		rows = append(rows, []string{
			as.Agent,
			yesNo(as.Detected),
			yesNo(as.Installed),
			yesNo(as.Valid),
			yesNo(as.Derives),
			fmt.Sprintf("%d", as.Recorded),
			fmt.Sprintf("%d", as.Sessions),
			fmt.Sprintf("%d", as.Errors),
			orDash(as.LastEvent),
		})
	}
	printer.Table(headers, rows)
	return nil
}

// collectAgentStatus assembles the per-agent status row from the installer's
// detect/status probes plus the (optional) store's recorded-trace counters.
func collectAgentStatus(cmd *cobra.Command, s store.Store, inst ops.HookInstaller) agentStatus {
	as := agentStatus{Agent: inst.Name(), DisplayName: inst.DisplayName()}

	if det, dErr := inst.Detect(); dErr == nil {
		as.Detected = det.Detected
		as.ConfigPath = det.ConfigPath
		as.Version = det.Version
	}
	if st, sErr := inst.Status(); sErr == nil {
		as.Installed = st.Installed
		as.Valid = st.Valid
		as.Issues = st.Issues
	}
	// Whether captured raw traces for this agent can be projected into
	// spans. Without a deriver the agent is raw-only: hooks fire and rows
	// land, but `qvr audit logs`/`spans` and the UI timeline stay empty, so
	// "installed+valid" alone overstates how observable it is (#143).
	_, as.Derives = derive.Get(inst.Name())
	if s != nil {
		if ts, lErr := s.LatestRawAt(cmd.Context(), inst.Name()); lErr == nil && ts != nil {
			as.LastEvent = ts.Local().Format(time.RFC3339)
		}
		if n, cErr := s.CountRawSessions(cmd.Context(), nil, inst.Name()); cErr == nil {
			as.Sessions = n
		}
		if n, cErr := s.CountRawTraces(cmd.Context(), nil, inst.Name()); cErr == nil {
			as.Recorded = n
		}
		if n, cErr := s.CountSelfAuditErrors(cmd.Context(), inst.Name()); cErr == nil {
			as.Errors = n
		}
	}
	// Flag raw-only agents that are actually capturing: the user sees rows
	// pile up but no derived views, so name the cause instead of letting
	// INSTALLED=yes imply full observability.
	if as.Installed && !as.Derives && as.Recorded > 0 {
		as.Issues = append(as.Issues, "raw-only: no span deriver — logs/spans/UI timeline stay empty")
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
