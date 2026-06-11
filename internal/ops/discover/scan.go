package discover

import (
	"context"
	"time"

	"github.com/astra-sh/qvr/internal/ops/rawtrace"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/google/uuid"
)

// Store is the persistence surface a scan needs (interface-in-consumer): the
// scan ledger plus everything rawtrace.Ingest uses underneath.
type Store interface {
	rawtrace.Store
	GetScannedFiles(ctx context.Context, agent string) (map[string]*store.ScannedFile, error)
	UpsertScannedFile(ctx context.Context, f *store.ScannedFile) error
}

// Options tunes one discovery scan.
type Options struct {
	Agents []string  // restrict to these canonical agent names (empty = all scannable)
	Since  time.Time // skip files last modified before this (zero = no cutoff)
	// KeepAll disables the skill gate: every discovered session is ingested,
	// matching explicit `qvr audit ingest` semantics.
	KeepAll bool
	// DryRun walks and stat-diffs but persists nothing — no rows, no ledger.
	DryRun bool
}

// AgentReport is one agent's scan outcome.
type AgentReport struct {
	Agent     string `json:"agent"`
	Seen      int    `json:"seen"`      // matching files found on disk
	Unchanged int    `json:"unchanged"` // skipped by the stat ledger
	Ingested  int    `json:"ingested"`  // files whose session was (re)ingested
	Skipped   int    `json:"skipped"`   // skill-gate skips (provably skill-less)
	Errors    int    `json:"errors"`
	Lines     int    `json:"lines"` // raw rows stored
	Spans     int    `json:"spans"`
	// WouldExamine counts the new/changed files a dry run would have ingested
	// (kept distinct from Ingested, which only ever counts persisted work).
	WouldExamine int `json:"would_examine,omitempty"`
}

// Report is the whole scan's outcome.
type Report struct {
	Agents []*AgentReport `json:"agents"`
	DryRun bool           `json:"dry_run,omitempty"`
}

// Totals sums the per-agent counters.
func (r *Report) Totals() AgentReport {
	var t AgentReport
	for _, a := range r.Agents {
		t.Seen += a.Seen
		t.Unchanged += a.Unchanged
		t.Ingested += a.Ingested
		t.Skipped += a.Skipped
		t.Errors += a.Errors
		t.Lines += a.Lines
		t.Spans += a.Spans
		t.WouldExamine += a.WouldExamine
	}
	return t
}

// Scan walks every scannable session store, stat-diffs each file against the
// ledger, and feeds new/changed files through the gated ingest pipeline. It is
// incremental and idempotent: an unchanged file costs one map lookup; a grown
// append-log ingests only its tail; a rewritten document replaces its rows.
func Scan(ctx context.Context, s Store, opts Options) (*Report, error) {
	rep := &Report{DryRun: opts.DryRun}
	for _, st := range Scannable(opts.Agents) {
		ar, err := scanStore(ctx, s, st, opts)
		if err != nil {
			return rep, err
		}
		rep.Agents = append(rep.Agents, ar)
	}
	return rep, nil
}

// scanStore scans one agent's store.
func scanStore(ctx context.Context, s Store, st SessionStore, opts Options) (*AgentReport, error) {
	ar := &AgentReport{Agent: st.Agent}
	if st.Layout == LayoutSQLite {
		// SQLite-backed stores need a dedicated reader (rowid watermarks, live
		// WAL writers); none is wired yet, so the row is inert.
		return ar, nil
	}
	candidates, err := enumerate(st, opts.Since)
	if err != nil {
		return ar, err
	}
	ar.Seen = len(candidates)
	if len(candidates) == 0 {
		return ar, nil
	}

	ledger, err := s.GetScannedFiles(ctx, st.Agent)
	if err != nil {
		return ar, err
	}
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return ar, err
		}
		// The stat gate never caches an error outcome: a transient ingest
		// failure must retry next scan even when the file hasn't changed
		// (document-layout files are rewritten atomically, so a failed one
		// may never change again).
		if prev := ledger[c.path]; prev != nil && prev.Status != store.ScanStatusError &&
			prev.Size == c.size && prev.MtimeMs == c.mtimeMs {
			ar.Unchanged++
			continue
		}
		if opts.DryRun {
			ar.WouldExamine++
			continue
		}
		scanOneFile(ctx, s, st, c, opts, ar)
	}
	return ar, nil
}

// scanOneFile ingests one new/changed file and records the outcome in the
// ledger. Ingest errors are per-file (counted, never fatal to the scan).
func scanOneFile(ctx context.Context, s Store, st SessionStore, c candidate, opts Options, ar *AgentReport) {
	res, err := rawtrace.Ingest(ctx, s, rawtrace.IngestParams{
		Agent:     st.Agent,
		Path:      c.path,
		SkillGate: !opts.KeepAll,
		Document:  st.Layout == LayoutDocument,
	})

	entry := &store.ScannedFile{
		AgentName:  st.Agent,
		SourcePath: c.path,
		Size:       c.size,
		MtimeMs:    c.mtimeMs,
	}
	switch {
	case err != nil:
		ar.Errors++
		entry.Status = store.ScanStatusError
	case res.Skipped:
		ar.Skipped++
		entry.Status = store.ScanStatusSkipped
	default:
		ar.Ingested++
		ar.Lines += res.LinesStored
		ar.Spans += res.SpansStored
		entry.Status = store.ScanStatusIngested
		entry.SessionID = res.SessionID
	}
	if entry.Status == store.ScanStatusError {
		entry.SessionID = uuid.Nil
	}
	// A ledger write failure is non-fatal: the worst case is re-examining the
	// file next scan, which the cursor/replace semantics make idempotent.
	_ = s.UpsertScannedFile(ctx, entry)
}
