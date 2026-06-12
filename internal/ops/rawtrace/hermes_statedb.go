package rawtrace

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/redact"
	"github.com/astra-sh/qvr/internal/ops/store"

	// Register the pure-Go "sqlite" driver with database/sql.
	_ "modernc.org/sqlite"
)

// Hermes persists sessions in ~/.hermes/state.db (SQLite, WAL) — there is no
// per-session transcript file to tail, so capture reads the database itself.
// Schema observed on a live store (Hermes v0.16.0, 2026-06-11; see
// docs F6 in the agent-tracing findings):
//
//	sessions(id TEXT '20260611_164635_70b54e', source, model, system_prompt,
//	         started_at, ended_at, message_count, tool_call_count,
//	         input_tokens, output_tokens, …, cwd, title, …)
//	messages(id INTEGER AUTOINCREMENT, session_id, role user|assistant|tool,
//	         content, tool_call_id, tool_calls JSON, tool_name,
//	         timestamp REAL epoch-seconds, token_count, finish_reason, …)
//
// The capture model maps onto the existing cursor machinery: each hermes
// session gets a virtual source path "<db>#<session-id>" whose cursor's
// ByteOffset is the max messages.id consumed (messages.id is the watermark —
// append-only per session). Rows are stored VERBATIM as the raw-canonical
// principle requires: each message row re-serialized as one JSON object with
// a "type":"message" envelope, preceded (on first capture) by a
// "type":"session" header row carrying the session columns the derivers and
// session meta need (model, cwd, title, token totals).

// IngestHermesStateDB captures every session in a hermes state.db that has
// new messages since its cursor. Gate semantics match discovery: when gate is
// true, a brand-new session that provably used no skill is skipped without
// persisting or advancing its cursor.
func IngestHermesStateDB(ctx context.Context, s Store, dbPath string, gate bool) ([]*Result, error) {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("rawtrace: open hermes state.db: %w", err)
	}
	defer db.Close()

	sessions, err := hermesSessions(ctx, db)
	if err != nil {
		return nil, err
	}
	var out []*Result
	var firstErr error
	for _, hs := range sessions {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		res, err := ingestHermesSession(ctx, s, db, dbPath, hs, gate)
		if err != nil {
			// Isolate per-session failures: a persistently bad session must
			// not block the ones after it. The first error still surfaces so
			// the scan ledger marks the file for re-examination.
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if res != nil {
			out = append(out, res)
		}
	}
	return out, firstErr
}

// hermesSession is the sessions-table subset capture reads.
type hermesSession struct {
	ID     string
	Header map[string]any // serialized as the "type":"session" header row
	Cwd    string
}

// hermesSessions lists the sessions in the db, newest last (stable ingest
// order). Columns are read generically so schema additions never break the
// reader — the header row carries whatever the table has.
func hermesSessions(ctx context.Context, db *sql.DB) ([]*hermesSession, error) {
	rows, err := db.QueryContext(ctx, `SELECT * FROM sessions ORDER BY started_at`)
	if err != nil {
		return nil, fmt.Errorf("rawtrace: hermes sessions: %w", err)
	}
	defer rows.Close()

	var out []*hermesSession
	for rows.Next() {
		m, err := scanRowMap(rows)
		if err != nil {
			return nil, err
		}
		id, _ := m["id"].(string)
		if id == "" {
			continue
		}
		cwd, _ := m["cwd"].(string)
		// The system prompt is persona text, not session evidence — keep the
		// header row lean (it is stored verbatim in the capture DB).
		delete(m, "system_prompt")
		m["type"] = "session"
		out = append(out, &hermesSession{ID: id, Header: m, Cwd: cwd})
	}
	return out, rows.Err()
}

// ingestHermesSession captures one session's new messages past its cursor.
// Returns nil when there is nothing new.
func ingestHermesSession(ctx context.Context, s Store, db *sql.DB, dbPath string, hs *hermesSession, gate bool) (*Result, error) {
	sourcePath := dbPath + "#" + hs.ID
	offset, err := s.GetRawCursor(ctx, "hermes", sourcePath)
	if err != nil {
		return nil, err
	}

	msgs, maxID, err := hermesMessages(ctx, db, hs.ID, offset)
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, nil
	}

	sessionID, agentSessionID := resolveSession(hs.ID)
	now := time.Now().UTC()
	mkRow := func(off int64, payload map[string]any) (*ops.RawTrace, error) {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("rawtrace: marshal hermes row: %w", err)
		}
		return &ops.RawTrace{
			AgentName:        "hermes",
			SessionID:        sessionID,
			AgentSessionID:   agentSessionID,
			Source:           ops.RawSourceTranscript,
			SourcePath:       sourcePath,
			WorkingDirectory: hs.Cwd,
			ByteOffset:       off,
			CapturedAt:       now,
			Raw:              redact.Bytes(raw),
		}, nil
	}

	var rows []*ops.RawTrace
	if offset == 0 {
		header, err := mkRow(0, hs.Header)
		if err != nil {
			return nil, err
		}
		rows = append(rows, header)
	}
	for _, m := range msgs {
		id, _ := m["id"].(int64)
		m["type"] = "message"
		r, err := mkRow(id, m)
		if err != nil {
			return nil, err
		}
		rows = append(rows, r)
	}

	res := &Result{SessionID: sessionID, TranscriptPath: sourcePath}
	if gate {
		allowed, gerr := skillGateAllows(ctx, s, IngestParams{Agent: "hermes"}, sessionID, sourcePath, rows)
		if gerr != nil {
			return nil, gerr
		}
		if !allowed {
			res.Skipped = true
			return res, nil // no rows, no cursor — self-healing when the session grows a skill use
		}
	}
	if err := s.AppendRawTraces(ctx, rows, &store.RawCursor{
		AgentName:  "hermes",
		SourcePath: sourcePath,
		ByteOffset: maxID,
		SessionID:  sessionID,
	}); err != nil {
		return nil, err
	}
	res.LinesStored = len(rows)
	n, _, derr := persistDerivation(ctx, s, sessionID)
	res.SpansStored = n
	res.SpanError = derr
	return res, nil
}

// hermesMessages reads one session's messages past the watermark, as generic
// column maps, returning the new max message id.
func hermesMessages(ctx context.Context, db *sql.DB, sessionID string, afterID int64) ([]map[string]any, int64, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT * FROM messages WHERE session_id = ? AND id > ? ORDER BY id`, sessionID, afterID)
	if err != nil {
		return nil, 0, fmt.Errorf("rawtrace: hermes messages: %w", err)
	}
	defer rows.Close()

	var out []map[string]any
	maxID := afterID
	for rows.Next() {
		m, err := scanRowMap(rows)
		if err != nil {
			return nil, 0, err
		}
		if id, ok := m["id"].(int64); ok && id > maxID {
			maxID = id
		}
		// tool_calls is stored as a JSON string column; inline it so the
		// captured row is one self-contained JSON object.
		if s, ok := m["tool_calls"].(string); ok && s != "" {
			var v any
			if json.Unmarshal([]byte(s), &v) == nil {
				m["tool_calls"] = v
			}
		}
		out = append(out, m)
	}
	return out, maxID, rows.Err()
}

// scanRowMap scans the current row into a column→value map, normalizing
// []byte to string so the result marshals as JSON text rather than base64.
func scanRowMap(rows *sql.Rows) (map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, fmt.Errorf("rawtrace: scan hermes row: %w", err)
	}
	m := make(map[string]any, len(cols))
	for i, c := range cols {
		v := vals[i]
		if b, ok := v.([]byte); ok {
			v = string(b)
		}
		m[c] = v
	}
	return m, nil
}
