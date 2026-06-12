package rawtrace

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/redact"

	// Register the pure-Go "sqlite" driver with database/sql.
	_ "modernc.org/sqlite"
)

// IngestOpencodeDB captures every session in an OpenCode database.
//
// OpenCode (v1.17+) persists sessions in a single SQLite database
// (~/.local/share/opencode/opencode.db, WAL) — NOT the per-file
// storage/session trees its earlier docs describe. Schema observed live
// (2026-06-11):
//
//	session(id 'ses_…', directory, title, agent, model JSON, cost,
//	        tokens_input/output/…, time_created, time_updated, …)
//	message(id 'msg_…', session_id, time_created, time_updated,
//	        data JSON {role, model:{providerID,modelID}, path:{cwd}, …})
//	part(id 'prt_…', message_id, session_id, time_created, time_updated,
//	     data JSON {type: text|reasoning|tool, tool: skill|read|bash,
//	                callID, state:{status, input:{…}, output}})
//
// Parts MUTATE in place (state running→completed), so the capture model is
// rewrite-per-session: whenever the db file's stat changes, every session is
// re-captured with replace semantics — verbatim rows are one "type":"session"
// header plus each message and part row JSON-wrapped; span ids are
// deterministic so re-derivation is idempotent. The skill signal is native:
// a part with tool:"skill" and state.input.name.
func IngestOpencodeDB(ctx context.Context, s Store, dbPath string, gate bool) ([]*Result, error) {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("rawtrace: open opencode.db: %w", err)
	}
	defer db.Close()

	sessions, err := db.QueryContext(ctx, `SELECT * FROM session ORDER BY time_created`)
	if err != nil {
		return nil, fmt.Errorf("rawtrace: opencode sessions: %w", err)
	}
	defer sessions.Close()

	var metas []map[string]any
	for sessions.Next() {
		m, err := scanRowMap(sessions)
		if err != nil {
			return nil, err
		}
		metas = append(metas, m)
	}
	if err := sessions.Err(); err != nil {
		return nil, err
	}
	// Release the cursor's connection before the per-session ingest loop
	// queries the same handle (mirrors the hermes reader's isolation).
	sessions.Close()

	var out []*Result
	var firstErr error
	for _, m := range metas {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		res, err := ingestOpencodeSession(ctx, s, db, dbPath, m, gate)
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

// addOpencodeTable streams one table's session rows into the capture via add.
// message and part both key rows by time-prefixed ids, so id order is
// creation order.
func addOpencodeTable(ctx context.Context, db *sql.DB, id, tbl string, add func(map[string]any) error) error {
	// tbl is interpolated into the query — keep the accepted set explicit so
	// the no-injection invariant survives new callers.
	if tbl != "message" && tbl != "part" {
		return fmt.Errorf("rawtrace: opencode: unexpected table %q", tbl)
	}
	r, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT * FROM %s WHERE session_id = ? ORDER BY id`, tbl), id)
	if err != nil {
		return fmt.Errorf("rawtrace: opencode %s: %w", tbl, err)
	}
	defer r.Close()
	for r.Next() {
		m, err := scanRowMap(r)
		if err != nil {
			return err
		}
		m["type"] = tbl
		// data is a TEXT column holding JSON — inline it so the captured
		// row is one self-contained JSON object, not a quoted string.
		if s, ok := m["data"].(string); ok && s != "" {
			var v any
			if json.Unmarshal([]byte(s), &v) == nil {
				m["data"] = v
			}
		}
		if err := add(m); err != nil {
			return err
		}
	}
	return r.Err()
}

// ingestOpencodeSession re-captures one session (replace semantics).
func ingestOpencodeSession(ctx context.Context, s Store, db *sql.DB, dbPath string, sess map[string]any, gate bool) (*Result, error) {
	id, _ := sess["id"].(string)
	if id == "" {
		return nil, nil
	}
	sourcePath := dbPath + "#" + id
	cwd, _ := sess["directory"].(string)
	sessionID, agentSessionID := resolveSession(id)
	now := time.Now().UTC()

	var rows []*ops.RawTrace
	seq := int64(0)
	add := func(payload map[string]any) error {
		raw, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("rawtrace: marshal opencode row: %w", err)
		}
		rows = append(rows, &ops.RawTrace{
			AgentName:        "opencode",
			SessionID:        sessionID,
			AgentSessionID:   agentSessionID,
			Source:           ops.RawSourceTranscript,
			SourcePath:       sourcePath,
			WorkingDirectory: cwd,
			ByteOffset:       seq,
			CapturedAt:       now,
			Raw:              redact.Bytes(raw),
		})
		seq++
		return nil
	}

	sess["type"] = "session"
	if err := add(sess); err != nil {
		return nil, err
	}
	for _, tbl := range []string{"message", "part"} {
		if err := addOpencodeTable(ctx, db, id, tbl, add); err != nil {
			return nil, err
		}
	}
	if len(rows) <= 1 {
		return nil, nil // header only — nothing to derive
	}

	res := &Result{SessionID: sessionID, TranscriptPath: sourcePath}
	if gate {
		allowed, gerr := skillGateAllows(ctx, s, IngestParams{Agent: "opencode", Document: true}, sessionID, sourcePath, rows)
		if gerr != nil {
			return nil, gerr
		}
		if !allowed {
			res.Skipped = true
			return res, nil
		}
	}
	if err := s.ReplaceSourceRawTraces(ctx, "opencode", sourcePath, rows); err != nil {
		return nil, err
	}
	res.LinesStored = len(rows)
	n, _, derr := persistDerivation(ctx, s, sessionID)
	res.SpansStored = n
	res.SpanError = derr
	return res, nil
}
