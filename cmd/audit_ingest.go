package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops/rawtrace"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/spf13/cobra"
)

var (
	ingestAgent   string
	ingestSession string
	ingestCwd     string
)

var auditIngestCmd = &cobra.Command{
	Use:    "ingest <transcript|rollout|dir> [more...]",
	Hidden: true, // low-level plumbing — see `qvr audit --help`
	Short:  "Record a session from an existing transcript, no live hook",
	Long: `Capture one or more already-produced transcripts as audit sessions WITHOUT
installing hooks into a live agent config. This is the qvr-native path for
QA / CI / sandboxed capture: a hook firing only points qvr at the agent's
transcript, so a transcript on its own is enough to record the session, derive
spans, and view it in 'qvr ui' — no need to mutate ~/.claude/settings.json or
$CODEX_HOME.

Pass a codex rollout, a claude session transcript (a .jsonl file), or a
directory of them (every *.jsonl inside is ingested as its own session). The
agent format is sniffed from the file when --agent is omitted; pass --agent to
force it. Ingest is idempotent and incremental: re-running over a file that has
since grown appends only the new tail.

Examples:
  qvr audit ingest ~/.codex/sessions/rollout-2026-06-02-019e88f6.jsonl
  qvr audit ingest --agent claude-code ~/.claude/projects/<slug>/<uuid>.jsonl
  qvr audit ingest ~/.codex/sessions/`,
	Args: cobra.MinimumNArgs(1),
	RunE: runAuditIngest,
}

func init() {
	f := auditIngestCmd.Flags()
	f.StringVar(&ingestAgent, "agent", "", "agent format of the transcript(s) (sniffed when omitted)")
	f.StringVar(&ingestSession, "session", "", "override the correlated session id (single-file ingest only)")
	f.StringVar(&ingestCwd, "cwd", "", "override the recorded working directory (for project scoping)")
	auditCmd.AddCommand(auditIngestCmd)
}

func runAuditIngest(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	files, err := expandIngestArgs(args)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no transcript files found in %v — expected .jsonl", args)
	}
	if ingestSession != "" && len(files) > 1 {
		return fmt.Errorf("--session applies to a single transcript, but %d were resolved", len(files))
	}

	// Open read-write so the DB is created+migrated on demand — ingest is a
	// deliberate import that should work even before the first hook event.
	s, err := openAuditStore(cmd.Context(), cfg, false)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	results := make([]ingestedResult, 0, len(files))
	var failed int

	for _, path := range files {
		rec := ingestOneFile(cmd, s, path)
		if rec.Error != "" {
			failed++
		}
		results = append(results, rec)
	}

	if outputFormat == "json" {
		if err := printer.JSON(map[string]any{"ingested": results, "failed": failed}); err != nil {
			return err
		}
		return ingestExit(failed, len(results))
	}

	renderIngestTable(results)
	if failed == 0 {
		printer.Success(fmt.Sprintf("Ingested %s", output.Plural(len(results), "session")))
		printer.Hint("view with `qvr ui` or `qvr audit spans --session <id>`")
	}
	return ingestExit(failed, len(results))
}

// ingestedResult is one per-file outcome row of `qvr audit ingest`.
type ingestedResult struct {
	Path      string `json:"path"`
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
	Lines     int    `json:"lines"`
	Spans     int    `json:"spans"`
	Error     string `json:"error,omitempty"`
}

// ingestOneFile sniffs the agent (unless forced) and ingests a single
// transcript, returning the outcome row (with Error set on failure).
func ingestOneFile(cmd *cobra.Command, s store.Store, path string) ingestedResult {
	agent := ingestAgent
	if agent == "" {
		agent = rawtrace.SniffAgent(path)
	}
	rec := ingestedResult{Path: path, Agent: agent}
	if agent == "" {
		rec.Error = "could not sniff agent — pass --agent"
		return rec
	}
	res, ierr := rawtrace.Ingest(cmd.Context(), s, rawtrace.IngestParams{
		Agent:      agent,
		Path:       path,
		SessionID:  ingestSession,
		WorkingDir: ingestCwd,
	})
	if ierr != nil {
		rec.Error = ierr.Error()
		return rec
	}
	rec.SessionID = res.SessionID.String()
	rec.Lines = res.LinesStored
	rec.Spans = res.SpansStored
	return rec
}

// renderIngestTable prints the per-file ingest results as a table.
func renderIngestTable(results []ingestedResult) {
	headers := []string{"AGENT", "LINES", "SPANS", "SESSION ID", "SOURCE"}
	rows := make([][]string, 0, len(results))
	for _, r := range results {
		src := filepath.Base(r.Path)
		if r.Error != "" {
			rows = append(rows, []string{r.Agent, "-", "-", "FAILED", src + " — " + r.Error})
			continue
		}
		rows = append(rows, []string{r.Agent, fmt.Sprintf("%d", r.Lines), fmt.Sprintf("%d", r.Spans), r.SessionID, src})
	}
	printer.Table(headers, rows)
}

// ingestExit returns a non-nil error (non-zero exit) when every file failed, so
// scripts can detect a wholesale failure; a partial success exits 0 with the
// per-file errors shown in the table.
func ingestExit(failed, total int) error {
	if total > 0 && failed == total {
		return fmt.Errorf("ingest failed for all %s", output.Plural(total, "file"))
	}
	return nil
}

// expandIngestArgs turns the positional args (files and/or directories) into a
// flat list of transcript files. A directory contributes every *.jsonl it
// holds; a file is taken as-is. A missing path is an error.
func expandIngestArgs(args []string) ([]string, error) {
	var files []string
	for _, a := range args {
		info, err := os.Stat(a)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", a, err)
		}
		if !info.IsDir() {
			files = append(files, a)
			continue
		}
		matches, err := filepath.Glob(filepath.Join(a, "*.jsonl"))
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", a, err)
		}
		files = append(files, matches...)
	}
	return files, nil
}
