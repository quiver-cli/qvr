package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/ops"
	"github.com/raks097/quiver/internal/ops/store"
	"github.com/spf13/cobra"
)

var auditStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show per-agent hook + capture status",
	Long: `Reports, for every installable agent: whether it's detected on this
machine, whether Quiver's hooks are installed and valid, how many events and
sessions have been recorded, how many hook errors were logged, and when the
last event for that agent was recorded. RECORDED counts individual events
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
		if s != nil {
			if ts := lastEventTime(cmd.Context(), s, inst.Name()); ts != nil {
				as.LastEvent = ts.Local().Format(time.RFC3339)
			}
			if n, cErr := s.CountSessions(cmd.Context(), inst.Name()); cErr == nil {
				as.Sessions = n
			}
			if n, cErr := s.CountEvents(cmd.Context(), inst.Name()); cErr == nil {
				as.Recorded = n
			}
			if n, cErr := s.CountSelfAuditErrors(cmd.Context(), inst.Name()); cErr == nil {
				as.Errors = n
			}
		}
		statuses = append(statuses, as)
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
	headers := []string{"AGENT", "DETECTED", "INSTALLED", "VALID", "RECORDED", "SESSIONS", "ERRORS", "LAST EVENT"}
	rows := make([][]string, 0, len(statuses))
	for _, as := range statuses {
		rows = append(rows, []string{
			as.Agent,
			yesNo(as.Detected),
			yesNo(as.Installed),
			yesNo(as.Valid),
			fmt.Sprintf("%d", as.Recorded),
			fmt.Sprintf("%d", as.Sessions),
			fmt.Sprintf("%d", as.Errors),
			orDash(as.LastEvent),
		})
	}
	printer.Table(headers, rows)
	return nil
}

// lastEventTime returns the timestamp of the newest event for agent, or nil.
func lastEventTime(ctx context.Context, s store.Store, agent string) *time.Time {
	events, err := s.QueryEvents(ctx, &store.EventFilter{Agents: []string{agent}, Limit: 1})
	if err != nil || len(events) == 0 {
		return nil
	}
	return &events[0].Timestamp
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
