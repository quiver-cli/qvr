package cmd

import (
	"fmt"
	"strings"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops/discover"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/spf13/cobra"
)

var (
	discoverAgents  []string
	discoverSince   string
	discoverKeepAll bool
	discoverDryRun  bool
)

var auditDiscoverCmd = &cobra.Command{
	Use:   "discover",
	Short: "Scan agents' native session stores and record skill sessions",
	Long: `Discover walks the session stores agents already keep on disk (Claude
Code's ~/.claude/projects, Codex's rollout files, …), and records every
skill-using session it finds — past and present, no agent configuration
touched. This is the zero-setup way to populate the audit database: run it
once and months of existing history back-fill instantly.

Scans are incremental: a stat ledger remembers every file seen, so an
unchanged store costs almost nothing and re-running is always safe. Sessions
that provably used no skill are counted but not stored (qvr keeps
skill-attributed evidence, not generic transcripts); pass --keep-all to
import everything. Only agents qvr can derive spans for are scanned.`,
	Args: cobra.NoArgs,
	RunE: runAuditDiscover,
}

func init() {
	f := auditDiscoverCmd.Flags()
	f.StringSliceVar(&discoverAgents, "agent", nil, "only scan these agents (e.g. claude, codex)")
	f.StringVar(&discoverSince, "since", "", "only files modified since this time (e.g. 90d, 24h, or RFC3339)")
	f.BoolVar(&discoverKeepAll, "keep-all", false, "also record sessions that used no skill")
	f.BoolVar(&discoverDryRun, "dry-run", false, "report what would be scanned without storing anything")
	auditCmd.AddCommand(auditDiscoverCmd)
}

func runAuditDiscover(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	opts := discover.Options{KeepAll: discoverKeepAll, DryRun: discoverDryRun}
	for _, a := range discoverAgents {
		opts.Agents = append(opts.Agents, canonicalAgentFlag(a))
	}
	if discoverSince != "" {
		t, perr := parseTimeFlag(discoverSince)
		if perr != nil {
			return fmt.Errorf("invalid --since: %w", perr)
		}
		opts.Since = t
	}

	// Open read-write so the DB is created+migrated on demand — like ingest,
	// discovery is a deliberate import that works before `qvr audit enable`.
	s, err := openAuditStore(cmd.Context(), cfg, false)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	rep, err := discover.Scan(cmd.Context(), s, opts)
	if err != nil {
		return fmt.Errorf("discover: %w", err)
	}

	if outputFormat == "json" {
		return printer.JSON(rep)
	}
	renderDiscoverReport(rep)
	return nil
}

// renderDiscoverReport prints the per-agent scan table plus a summary line.
func renderDiscoverReport(rep *discover.Report) {
	headers := []string{"AGENT", "SEEN", "UNCHANGED", "INGESTED", "SKIPPED", "ERRORS", "LINES", "SPANS"}
	rows := make([][]string, 0, len(rep.Agents))
	for _, a := range rep.Agents {
		rows = append(rows, []string{
			a.Agent,
			fmt.Sprintf("%d", a.Seen),
			fmt.Sprintf("%d", a.Unchanged),
			fmt.Sprintf("%d", a.Ingested),
			fmt.Sprintf("%d", a.Skipped),
			fmt.Sprintf("%d", a.Errors),
			fmt.Sprintf("%d", a.Lines),
			fmt.Sprintf("%d", a.Spans),
		})
	}
	printer.Table(headers, rows)

	t := rep.Totals()
	if rep.DryRun {
		printer.Info(fmt.Sprintf("Dry run: %s would be examined (%d unchanged)",
			output.Plural(t.WouldExamine, "file"), t.Unchanged))
		return
	}
	parts := []string{fmt.Sprintf("recorded %s", output.Plural(t.Ingested, "session"))}
	if t.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d without skill usage (not stored)", t.Skipped))
	}
	if t.Errors > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", t.Errors))
	}
	printer.Success("Discover: " + strings.Join(parts, ", "))
	if t.Ingested > 0 {
		printer.Hint("view with `qvr ui` or `qvr audit sessions`")
	}
}
