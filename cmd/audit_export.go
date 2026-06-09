package cmd

import (
	"bufio"
	"fmt"
	"os"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var (
	exportAgent   string
	exportSession string
	exportSource  string
	exportOut     string
)

var auditExportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export captured raw traces as JSONL",
	Long: `Streams matching raw trace rows as one JSON object per line — the
agent's own transcript lines and hook payloads, verbatim. Suitable for
archival, external analysis, or replay. Use --source to restrict to transcript
lines or hook payloads.`,
	Args: cobra.NoArgs,
	RunE: runAuditExport,
}

func init() {
	f := auditExportCmd.Flags()
	f.StringVar(&exportAgent, "agent", "", "filter by agent name")
	f.StringVar(&exportSession, "session", "", "filter by session id")
	f.StringVar(&exportSource, "source", "", "filter by source (transcript | hook_payload)")
	f.StringVarP(&exportOut, "out", "o", "", "write to this file instead of stdout")
	auditCmd.AddCommand(auditExportCmd)
}

// buildExportFilter assembles the raw-trace filter from the export flags,
// validating the --session id when given.
func buildExportFilter() (*store.RawTraceFilter, error) {
	filter := &store.RawTraceFilter{}
	if exportAgent != "" {
		filter.Agents = []string{exportAgent}
	}
	if exportSource != "" {
		filter.Sources = []string{exportSource}
	}
	if exportSession != "" {
		id, perr := uuid.Parse(exportSession)
		if perr != nil {
			return nil, fmt.Errorf("invalid --session id %q: %w", exportSession, perr)
		}
		filter.SessionID = &id
	}
	return filter, nil
}

func runAuditExport(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	filter, err := buildExportFilter()
	if err != nil {
		return err
	}

	var w *bufio.Writer
	if exportOut != "" {
		f, oErr := os.Create(exportOut)
		if oErr != nil {
			return fmt.Errorf("create %s: %w", exportOut, oErr)
		}
		defer f.Close()
		w = bufio.NewWriter(f)
	} else {
		w = bufio.NewWriter(os.Stdout)
	}

	if !auditDBExists(cfg) {
		if exportOut != "" {
			printer.Info("No traces to export")
		}
		return nil
	}

	s, err := openAuditStore(cmd.Context(), cfg, true)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	count := 0
	err = s.StreamRawTraces(cmd.Context(), filter, func(r *ops.RawTrace) error {
		// Emit the verbatim native bytes (valid JSON for transcript lines and
		// hook payloads), one per line.
		if _, wErr := w.Write(r.Raw); wErr != nil {
			return wErr
		}
		if _, wErr := w.WriteString("\n"); wErr != nil {
			return wErr
		}
		count++
		return nil
	})
	if err != nil {
		return fmt.Errorf("export traces: %w", err)
	}

	// Surface any buffered-write error rather than dropping it on a deferred
	// flush — a truncated export must not report success.
	if err := w.Flush(); err != nil {
		return fmt.Errorf("flush export: %w", err)
	}

	if exportOut != "" {
		printer.Success(fmt.Sprintf("Exported %s to %s", output.Plural(count, "trace"), exportOut))
	}
	return nil
}
