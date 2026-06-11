package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SpanRow is a persisted derived span. It is the storage form of a
// derive.Span (the derive package maps to/from this so the store stays
// independent of the derive layer). Attributes is the OpenTelemetry gen_ai.*
// attribute map serialized as JSON.
type SpanRow struct {
	SpanID         string    `json:"span_id"`
	TraceID        string    `json:"trace_id"`
	ParentSpanID   string    `json:"parent_span_id,omitempty"`
	SessionID      uuid.UUID `json:"session_id"`
	AgentName      string    `json:"agent_name"`
	Kind           string    `json:"kind"`
	Name           string    `json:"name"`
	StartMs        int64     `json:"start_ms"`
	EndMs          int64     `json:"end_ms"`
	Attributes     string    `json:"attributes"` // JSON
	DeriverVersion int       `json:"deriver_version"`
	DerivedAt      time.Time `json:"derived_at"`
}

// SpanFilter scopes QuerySpans. Nil/zero fields are ignored.
type SpanFilter struct {
	SessionID *uuid.UUID
	Agents    []string
	Kinds     []string
	Limit     int
}

const insertSpanSQL = `INSERT INTO spans(
  span_id, trace_id, parent_span_id, session_id, agent_name, kind, name,
  start_ms, end_ms, attributes, deriver_version, derived_at
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`

// ReplaceSessionSpans atomically replaces all stored spans for a session with
// rows. Replacement (not upsert) keeps the stored set exactly in sync with the
// current derivation, even when a re-derive yields fewer spans. A nil/empty
// rows slice just clears the session's spans.
func (s *sqliteStore) ReplaceSessionSpans(ctx context.Context, sessionID uuid.UUID, rows []*SpanRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: spans tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := replaceSessionSpansTx(ctx, tx, sessionID, rows); err != nil {
		return err
	}
	return tx.Commit()
}

// replaceSessionSpansTx is the shared span-replacement body, run inside the
// caller's tx (ReplaceSessionSpans and ReplaceSessionDerivation).
func replaceSessionSpansTx(ctx context.Context, tx *sql.Tx, sessionID uuid.UUID, rows []*SpanRow) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM spans WHERE session_id = ?`, sessionID.String()); err != nil {
		return fmt.Errorf("store: clear session spans: %w", err)
	}
	// Defensively drop duplicate span_ids (last write wins) before inserting:
	// span_id is UNIQUE, and a single colliding row from a deriver bug must not
	// fail the whole insert and lose the entire session's spans (#147). Derivers
	// are expected to emit unique ids; this is the belt-and-suspenders backstop.
	seen := make(map[string]struct{}, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		if r == nil {
			continue
		}
		if _, dup := seen[r.SpanID]; dup {
			rows[i] = nil // a later row already claimed this span_id
			continue
		}
		seen[r.SpanID] = struct{}{}
	}
	for _, r := range rows {
		if r == nil {
			continue
		}
		if r.DerivedAt.IsZero() {
			r.DerivedAt = time.Now().UTC()
		}
		if _, err := tx.ExecContext(ctx, insertSpanSQL,
			r.SpanID, r.TraceID, nullableString(r.ParentSpanID), sessionID.String(),
			r.AgentName, r.Kind, nullableString(r.Name),
			r.StartMs, r.EndMs, nullableString(r.Attributes), r.DeriverVersion, r.DerivedAt.UTC(),
		); err != nil {
			return fmt.Errorf("store: insert span: %w", err)
		}
	}
	return nil
}

const spanColumns = `span_id, trace_id, parent_span_id, session_id, agent_name,
  kind, name, start_ms, end_ms, attributes, deriver_version, derived_at`

// QuerySpans returns stored spans ordered by (session_id, start_ms).
func (s *sqliteStore) QuerySpans(ctx context.Context, f *SpanFilter) ([]*SpanRow, error) {
	var clauses []string
	var args []any
	if f != nil {
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
		if len(f.Kinds) > 0 {
			clauses = append(clauses, "kind IN ("+placeholders(len(f.Kinds))+")")
			for _, k := range f.Kinds {
				args = append(args, k)
			}
		}
	}
	q := `SELECT ` + spanColumns + ` FROM spans`
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	q += " ORDER BY session_id ASC, start_ms ASC"
	if f != nil && f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: query spans: %w", err)
	}
	defer rows.Close()

	var out []*SpanRow
	for rows.Next() {
		r, err := scanSpan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanSpan(rows interface {
	Scan(...any) error
}) (*SpanRow, error) {
	var (
		r                  SpanRow
		sid                string
		parent, name, attr sql.NullString
		derivedAt          time.Time
	)
	if err := rows.Scan(&r.SpanID, &r.TraceID, &parent, &sid, &r.AgentName,
		&r.Kind, &name, &r.StartMs, &r.EndMs, &attr, &r.DeriverVersion, &derivedAt); err != nil {
		return nil, fmt.Errorf("store: scan span: %w", err)
	}
	id, err := uuid.Parse(sid)
	if err != nil {
		return nil, fmt.Errorf("store: bad span session_id %q: %w", sid, err)
	}
	r.SessionID = id
	r.ParentSpanID = parent.String
	r.Name = name.String
	r.Attributes = attr.String
	r.DerivedAt = derivedAt.UTC()
	return &r, nil
}
