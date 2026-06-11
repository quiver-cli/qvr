package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/astra-sh/qvr/internal/ops"
	"github.com/google/uuid"
)

// RawCursor records how far into a transcript file capture has consumed.
// One row per (AgentName, SourcePath). Persisted in the same transaction as
// the raw rows it covers, so a crash never double-stores or skips bytes.
type RawCursor struct {
	AgentName  string
	SourcePath string
	ByteOffset int64
	SessionID  uuid.UUID
}

// RawTraceFilter selects rows for QueryRawTraces. Nil/zero fields are ignored.
type RawTraceFilter struct {
	SessionID *uuid.UUID
	Agents    []string
	Sources   []string // RawSourceTranscript / RawSourceHookPayload
	Since     *time.Time
	Until     *time.Time
	Limit     int
}

const appendRawTraceSQL = `INSERT INTO raw_traces(
  id, agent_name, session_id, agent_session_id, source, source_path,
  working_directory, hook_type, byte_offset, seq, captured_at, raw
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`

// AppendRawTraces stores rows verbatim and advances the tailing cursor in a
// single transaction. Sequence numbers are assigned per session as a dense
// monotonic run continuing from whatever is already stored, so capture order
// is preserved even across hook firings. A nil/empty rows slice with a non-nil
// cursor just advances the cursor (e.g. when a tail found only a partial line).
func (s *sqliteStore) AppendRawTraces(ctx context.Context, rows []*ops.RawTrace, cursor *RawCursor) error {
	if len(rows) == 0 && cursor == nil {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: raw tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Resolve the next sequence number per session once up front.
	seqFor := newRawSeqAllocator(ctx, tx)

	if err := insertRawTraceRows(ctx, tx, rows, seqFor); err != nil {
		return err
	}

	if cursor != nil {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO trace_cursors(agent_name, source_path, byte_offset, session_id, updated_at)
			 VALUES (?,?,?,?,?)
			 ON CONFLICT(agent_name, source_path)
			 DO UPDATE SET byte_offset = excluded.byte_offset,
			               session_id  = excluded.session_id,
			               updated_at  = excluded.updated_at`,
			cursor.AgentName, cursor.SourcePath, cursor.ByteOffset,
			nullableSessionID(cursor.SessionID), time.Now().UTC(),
		); err != nil {
			return fmt.Errorf("store: advance cursor: %w", err)
		}
	}

	return tx.Commit()
}

// ReplaceSourceRawTraces atomically replaces every raw row ingested from one
// source file with rows, in a single transaction. Document-layout stores
// (whole-file JSON rewritten in place, not appended) re-ingest this way so a
// failure can never leave the old rows deleted but the new ones missing.
func (s *sqliteStore) ReplaceSourceRawTraces(ctx context.Context, agent, sourcePath string, rows []*ops.RawTrace) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: replace source tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM raw_traces WHERE agent_name = ? AND source_path = ?`,
		agent, sourcePath); err != nil {
		return fmt.Errorf("store: replace source delete: %w", err)
	}
	if err := insertRawTraceRows(ctx, tx, rows, newRawSeqAllocator(ctx, tx)); err != nil {
		return err
	}
	return tx.Commit()
}

// newRawSeqAllocator returns a per-session sequence allocator that hands out a
// dense monotonic run continuing from whatever is already stored for a session,
// caching the next value per session so repeated calls within one tx stay dense.
func newRawSeqAllocator(ctx context.Context, tx *sql.Tx) func(sid uuid.UUID) (int, error) {
	nextSeq := map[uuid.UUID]int{}
	return func(sid uuid.UUID) (int, error) {
		if n, ok := nextSeq[sid]; ok {
			nextSeq[sid] = n + 1
			return n, nil
		}
		var maxSeq sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			`SELECT MAX(seq) FROM raw_traces WHERE session_id = ?`, sid.String(),
		).Scan(&maxSeq); err != nil {
			return 0, err
		}
		base := 0
		if maxSeq.Valid {
			base = int(maxSeq.Int64) + 1
		}
		nextSeq[sid] = base + 1
		return base, nil
	}
}

// insertRawTraceRows fills in defaults (id, captured_at), assigns each row its
// per-session sequence number via seqFor, and inserts it verbatim. A nil row is
// skipped; a row missing a session id is a hard error.
func insertRawTraceRows(ctx context.Context, tx *sql.Tx, rows []*ops.RawTrace, seqFor func(uuid.UUID) (int, error)) error {
	for _, r := range rows {
		if r == nil {
			continue
		}
		if r.ID == uuid.Nil {
			r.ID = uuid.New()
		}
		if r.SessionID == uuid.Nil {
			return errors.New("store: raw trace missing session_id")
		}
		if r.CapturedAt.IsZero() {
			r.CapturedAt = time.Now().UTC()
		}
		seq, err := seqFor(r.SessionID)
		if err != nil {
			return fmt.Errorf("store: raw seq: %w", err)
		}
		r.Seq = seq
		if _, err := tx.ExecContext(ctx, appendRawTraceSQL,
			r.ID.String(), r.AgentName, r.SessionID.String(), nullableString(r.AgentSessionID),
			r.Source, nullableString(r.SourcePath), nullableString(r.WorkingDirectory), nullableString(r.HookType),
			r.ByteOffset, r.Seq, r.CapturedAt.UTC(), r.Raw,
		); err != nil {
			return fmt.Errorf("store: append raw trace: %w", err)
		}
	}
	return nil
}

// GetRawCursor returns the byte offset capture last consumed for (agent,
// sourcePath), or 0 if this file has never been tailed.
func (s *sqliteStore) GetRawCursor(ctx context.Context, agent, sourcePath string) (int64, error) {
	var off int64
	err := s.db.QueryRowContext(ctx,
		`SELECT byte_offset FROM trace_cursors WHERE agent_name = ? AND source_path = ?`,
		agent, sourcePath,
	).Scan(&off)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("store: get cursor: %w", err)
	}
	return off, nil
}

const rawTraceColumns = `id, agent_name, session_id, agent_session_id, source,
  source_path, working_directory, hook_type, byte_offset, seq, captured_at, raw`

// QueryRawTraces returns rows ordered by (session_id, seq) ascending so a
// session reads back in capture order. Use a SessionID filter to scope to one
// session; otherwise rows from all sessions interleave by the sort.
func (s *sqliteStore) QueryRawTraces(ctx context.Context, f *RawTraceFilter) ([]*ops.RawTrace, error) {
	where, args := f.build()
	limit := 1000
	if f != nil && f.Limit > 0 {
		limit = f.Limit
	}
	q := `SELECT ` + rawTraceColumns + ` FROM raw_traces ` + where +
		` ORDER BY session_id ASC, seq ASC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: query raw traces: %w", err)
	}
	defer rows.Close()

	var out []*ops.RawTrace
	for rows.Next() {
		r, err := scanRawTrace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate raw traces: %w", err)
	}
	return out, nil
}

func (f *RawTraceFilter) build() (string, []any) {
	if f == nil {
		return "", nil
	}
	var clauses []string
	var args []any
	if f.SessionID != nil {
		clauses = append(clauses, "session_id = ?")
		args = append(args, f.SessionID.String())
	}
	if len(f.Agents) > 0 {
		clauses = append(clauses, "agent_name IN ("+placeholders(len(f.Agents))+")")
		for _, a := range f.Agents {
			args = append(args, a)
		}
	}
	if len(f.Sources) > 0 {
		clauses = append(clauses, "source IN ("+placeholders(len(f.Sources))+")")
		for _, src := range f.Sources {
			args = append(args, src)
		}
	}
	if f.Since != nil {
		clauses = append(clauses, "captured_at >= ?")
		args = append(args, f.Since.UTC())
	}
	if f.Until != nil {
		clauses = append(clauses, "captured_at <= ?")
		args = append(args, f.Until.UTC())
	}
	if len(clauses) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func scanRawTrace(rows *sql.Rows) (*ops.RawTrace, error) {
	var (
		r              ops.RawTrace
		id, sid        string
		agentSessionID sql.NullString
		sourcePath     sql.NullString
		workingDir     sql.NullString
		hookType       sql.NullString
		capturedAt     time.Time
	)
	if err := rows.Scan(
		&id, &r.AgentName, &sid, &agentSessionID, &r.Source,
		&sourcePath, &workingDir, &hookType, &r.ByteOffset, &r.Seq, &capturedAt, &r.Raw,
	); err != nil {
		return nil, fmt.Errorf("store: scan raw trace: %w", err)
	}
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("store: bad raw trace id %q: %w", id, err)
	}
	parsedSID, err := uuid.Parse(sid)
	if err != nil {
		return nil, fmt.Errorf("store: bad raw trace session_id %q: %w", sid, err)
	}
	r.ID = parsedID
	r.SessionID = parsedSID
	r.AgentSessionID = agentSessionID.String
	r.SourcePath = sourcePath.String
	r.WorkingDirectory = workingDir.String
	r.HookType = hookType.String
	r.CapturedAt = capturedAt.UTC()
	return &r, nil
}

// nullableSessionID maps the zero UUID to a SQL NULL so cursor rows for
// not-yet-correlated sessions don't store a bogus all-zero id.
func nullableSessionID(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id.String()
}
