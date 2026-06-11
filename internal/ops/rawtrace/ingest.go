package rawtrace

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/astra-sh/qvr/internal/ops/redact"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/google/uuid"
)

// IngestParams configures a one-shot ingest of an already-produced transcript.
type IngestParams struct {
	Agent      string // required: which agent's transcript format this is
	Path       string // the transcript/rollout file
	SessionID  string // optional: override the correlated session id
	WorkingDir string // optional: override the recorded working directory

	// SkillGate applies skill-only retention BEFORE persisting: the candidate
	// rows are derived in memory first, and a session that provably used no
	// skill is skipped — nothing is stored, no cursor advances. Sessions that
	// already have stored rows are always ingested (their retention was
	// decided when they were first kept). Discovery scans set this; explicit
	// `qvr audit ingest` does not (a deliberate import is always kept).
	SkillGate bool

	// Document marks a rewrite-in-place source (a whole JSON document that the
	// agent rewrites rather than appends to). Ingest then replaces the file's
	// previously stored rows instead of cursor-tailing, so a rewritten file
	// never duplicates rows.
	Document bool
}

// Ingest records an already-produced transcript as a captured session WITHOUT
// any live hook — the recovery/QA/CI counterpart to hook-driven Capture. It is
// the answer to "capture one session without persistently editing the live
// agent config" (#148): point it at a codex rollout or a claude session
// transcript and it stores the raw rows and derives spans, exactly as a hook
// firing would have.
//
// Like Capture it is cursor-based and idempotent: re-ingesting the same file
// stores only rows past the last byte offset, so importing a transcript that
// has since grown appends the new tail rather than duplicating. Unlike Capture
// it never applies the skill-only retention gate — an explicit ingest is a
// deliberate import, so the session is always kept even if it used no skill.
func Ingest(ctx context.Context, s Store, p IngestParams) (*Result, error) {
	if p.Agent == "" {
		return nil, fmt.Errorf("rawtrace: ingest requires an agent")
	}
	// Normalize the agent to its canonical target name so every row, cursor,
	// and projection is keyed consistently (aliases like "claude-code" fold in).
	if c, ok := model.CanonicalTarget(p.Agent); ok {
		p.Agent = c
	}
	path, err := filepath.Abs(expandHome(p.Path))
	if err != nil {
		return nil, fmt.Errorf("rawtrace: resolve path: %w", err)
	}
	if !fileExists(path) {
		return nil, fmt.Errorf("rawtrace: ingest source is not a file: %s", path)
	}

	sniff := sniffTranscript(path)
	rawID := firstNonEmpty(p.SessionID, sniff.sessionID, uuidInName(path), path)
	sessionID, agentSessionID := resolveSession(rawID)
	wd := firstNonEmpty(p.WorkingDir, sniff.cwd)

	res := &Result{SessionID: sessionID, TranscriptPath: path}

	rows, cursor, err := buildIngestRows(ctx, s, p, path, sessionID, agentSessionID, wd)
	if err != nil {
		return nil, err
	}

	if p.SkillGate && len(rows) > 0 {
		allowed, gerr := skillGateAllows(ctx, s, p, sessionID, path, rows)
		if gerr != nil {
			return nil, gerr
		}
		if !allowed {
			res.Skipped = true
			return res, nil // nothing persisted, no cursor advance — self-healing on growth
		}
	}

	if len(rows) > 0 {
		if p.Document {
			// Rewrite-in-place source: replace this file's prior rows in one
			// tx, so a rewritten document never duplicates and a failure never
			// strands the session between delete and insert.
			if err := s.ReplaceSourceRawTraces(ctx, p.Agent, path, rows); err != nil {
				return nil, err
			}
		} else if err := s.AppendRawTraces(ctx, rows, cursor); err != nil {
			return nil, err
		}
	}
	res.LinesStored = len(rows)

	// Always (re-)derive: a fresh ingest derives the new rows, and a no-op
	// re-ingest still refreshes the projection against the current deriver.
	n, _, derr := persistDerivation(ctx, s, sessionID)
	res.SpansStored = n
	res.SpanError = derr
	return res, nil
}

// buildIngestRows assembles the candidate raw rows (and tail cursor) for one
// source file. Append-log sources tail from the stored cursor; document
// sources contribute the whole file as a single verbatim row (no cursor).
func buildIngestRows(ctx context.Context, s Store, p IngestParams, path string, sessionID uuid.UUID, agentSessionID, wd string) ([]*ops.RawTrace, *store.RawCursor, error) {
	now := time.Now().UTC()
	base := func(offset int64, raw []byte) *ops.RawTrace {
		return &ops.RawTrace{
			AgentName:        p.Agent,
			SessionID:        sessionID,
			AgentSessionID:   agentSessionID,
			Source:           ops.RawSourceTranscript,
			SourcePath:       path,
			WorkingDirectory: wd,
			ByteOffset:       offset,
			CapturedAt:       now,
			Raw:              redact.Bytes(raw),
		}
	}

	if p.Document {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, fmt.Errorf("rawtrace: read document: %w", err)
		}
		if len(bytes.TrimSpace(data)) == 0 {
			return nil, nil, nil
		}
		return []*ops.RawTrace{base(0, data)}, nil, nil
	}

	lines, newOffset, err := tailTranscript(ctx, s, p.Agent, path)
	if err != nil {
		return nil, nil, err
	}
	rows := make([]*ops.RawTrace, 0, len(lines))
	for _, ln := range lines {
		rows = append(rows, base(ln.offset, ln.bytes))
	}
	if len(rows) == 0 {
		return nil, nil, nil
	}
	return rows, &store.RawCursor{
		AgentName:  p.Agent,
		SourcePath: path,
		ByteOffset: newOffset,
		SessionID:  sessionID,
	}, nil
}

// skillGateAllows decides whether a gated ingest may persist. A session that
// already has stored rows is always allowed (its retention was decided when it
// was first kept — and new tail rows for it must not be dropped). Otherwise
// the candidate rows are derived IN MEMORY: only a provably skill-attributed
// session passes. An agent with no deriver passes too — skill absence is
// unprovable there, mirroring the retention guard everywhere else.
//
// Gate-then-persist (rather than store-then-prune) is load-bearing:
// DeleteSession also deletes the tailing cursor, so pruning after storing
// would re-ingest the same skill-less file on every scan, forever.
func skillGateAllows(ctx context.Context, s Store, p IngestParams, sessionID uuid.UUID, path string, candidates []*ops.RawTrace) (bool, error) {
	existing, err := s.QueryRawTraces(ctx, &store.RawTraceFilter{SessionID: &sessionID})
	if err != nil {
		return false, err
	}
	for _, r := range existing {
		// Any stored row means the session was already kept once — new rows
		// for it must not be dropped. Exception: for a DOCUMENT re-ingest,
		// rows from the same path are about to be replaced, so they don't
		// count; the keep decision is re-made over the fresh content.
		if !p.Document || r.SourcePath != path {
			return true, nil
		}
	}
	d, derr := derive.DeriveSession(candidates)
	if derr != nil || d == nil {
		return true, nil // no deriver — unprovable, never drop
	}
	return hasSkillSpans(d.Spans), nil
}

// transcriptSniff holds the session id and working directory recovered from a
// transcript's own content, so an ingest correlates and scopes the session the
// same way a live hook capture would have.
type transcriptSniff struct {
	sessionID string
	cwd       string
}

// sniffTranscript reads the leading rows of a transcript looking for the
// agent's own session id and cwd, across the session-store shapes qvr knows:
// top-level sessionId/cwd lines, payload-wrapped session metadata, typed
// session-header records ({"type":"session"|"session_start","id","cwd"}), and
// data-wrapped session starts ({"type":"session.start","data":{"sessionId"}}).
// Best-effort: a file we can't read or parse yields an empty sniff.
func sniffTranscript(path string) transcriptSniff {
	f, err := os.Open(path)
	if err != nil {
		return transcriptSniff{}
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // transcript lines can be large
	var out transcriptSniff
	for i := 0; i < 200 && sc.Scan(); i++ {
		var v struct {
			Type        string `json:"type"`
			ID          string `json:"id"`
			SessionID   string `json:"session_id"`
			SessionIDCC string `json:"sessionId"`
			Cwd         string `json:"cwd"`
			Payload     struct {
				ID        string `json:"id"`
				SessionID string `json:"session_id"`
				Cwd       string `json:"cwd"`
			} `json:"payload"`
			Data struct {
				SessionID string `json:"sessionId"`
				Cwd       string `json:"cwd"`
			} `json:"data"`
		}
		if json.Unmarshal(sc.Bytes(), &v) != nil {
			continue
		}
		// A top-level id only identifies the session on a session-header
		// record; message records carry their own per-record ids.
		headerID := ""
		switch v.Type {
		case "session", "session_start":
			headerID = v.ID
		}
		if out.sessionID == "" {
			out.sessionID = firstNonEmpty(v.SessionID, v.SessionIDCC, headerID,
				v.Payload.ID, v.Payload.SessionID, v.Data.SessionID)
		}
		if out.cwd == "" {
			out.cwd = firstNonEmpty(v.Cwd, v.Payload.Cwd, v.Data.Cwd)
		}
		if out.sessionID != "" && out.cwd != "" {
			break
		}
	}
	return out
}

var uuidRe = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// uuidInName extracts a uuid embedded in a transcript filename — claude names a
// transcript "<session-uuid>.jsonl"; codex rollouts embed the uuid in the file
// name too. Returns "" when the name carries none.
func uuidInName(path string) string {
	return uuidRe.FindString(filepath.Base(path))
}

// SniffAgent guesses which agent produced a transcript from its first row shape:
// codex rollouts are a stream of {type: session_meta|turn_context|event_msg|
// response_item} envelopes; claude transcripts are {type: user|assistant|...}
// rows carrying a message object. Returns "" when it can't tell, so the caller
// can require an explicit --agent.
func SniffAgent(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for i := 0; i < 50 && sc.Scan(); i++ {
		var v struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
			Message json.RawMessage `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &v) != nil {
			continue
		}
		switch v.Type {
		case "session_meta", "turn_context", "event_msg", "response_item":
			return "codex"
		case "user", "assistant", "system", "summary":
			return "claude"
		}
		if len(v.Payload) > 0 {
			return "codex"
		}
		if len(v.Message) > 0 {
			return "claude"
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
