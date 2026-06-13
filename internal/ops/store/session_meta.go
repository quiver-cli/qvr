package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SessionMetaRow is the persisted unified session model — the one read model
// every consumer (UI, CLI, metrics) lists sessions from. It is the storage
// form of a derive.SessionMeta: like spans, a deterministic projection of the
// session's raw rows, regenerable via `qvr audit rederive`.
type SessionMetaRow struct {
	SessionID       uuid.UUID `json:"session_id"`
	AgentName       string    `json:"agent_name"`
	SourceSessionID string    `json:"source_session_id,omitempty"`
	SourcePath      string    `json:"source_path,omitempty"`
	WorkingDir      string    `json:"working_directory,omitempty"`
	GitBranch       string    `json:"git_branch,omitempty"`
	Model           string    `json:"model,omitempty"`
	Title           string    `json:"title,omitempty"`
	StartedMs       int64     `json:"started_ms"`
	EndedMs         int64     `json:"ended_ms"`
	Turns           int64     `json:"turns"`
	Tools           int64     `json:"tools"`
	Skills          []string  `json:"skills,omitempty"` // distinct skill names, first-use order
	// Session token totals. nil = the native store reported no usage on that
	// side (serialized as absent, rendered n/a — never 0).
	TokensIn       *int64    `json:"tokens_in,omitempty"`
	TokensOut      *int64    `json:"tokens_out,omitempty"`
	DeriverVersion int       `json:"deriver_version"`
	DerivedAt      time.Time `json:"derived_at"`
}

// DurationMs is the session's wall-clock duration in milliseconds (ended minus
// started), or 0 when the bounds are missing or non-positive (a single-event or
// still-in-flight session). Callers render 0 as "unknown", never as an instant.
func (m *SessionMetaRow) DurationMs() int64 {
	if m.EndedMs > m.StartedMs {
		return m.EndedMs - m.StartedMs
	}
	return 0
}

// SessionMetaFilter scopes ListSessionMeta. Nil/zero fields are ignored.
type SessionMetaFilter struct {
	Since *time.Time // sessions started at/after this time
	Until *time.Time // sessions started at/before this time
	Agent string
	Skill string // only sessions that used this skill
	// SkillsOnly restricts the result to sessions that used at least one skill.
	// The DB can still hold skill-less sessions (explicit ingests, in-flight
	// captures); this is the read-side surfacing filter. Ignored when Skill is
	// set (a specific skill already implies skill-bearing).
	SkillsOnly bool
	Dirs       []string // working_directory ∈ Dirs (empty = all)
	Limit      int
	// SortByTokens orders by total session tokens descending (NULLs last —
	// sessions whose store reported no usage sort below a genuine 0) instead
	// of newest-first. Server-side because Limit truncates: a client-side
	// sort over a truncated page can't find the maximum.
	SortByTokens bool
}

const sessionMetaColumns = `session_id, agent_name, source_session_id, source_path,
  working_directory, git_branch, model, title, started_ms, ended_ms,
  turns, tools, skills, tokens_in, tokens_out, deriver_version, derived_at`

// sessionMetaColumnsLegacy reads a DB that predates migration 0006 (opened
// read-only by `qvr ui --no-discover`, so the migration can't apply): the
// token columns come back as NULL (n/a) instead of failing the whole list.
const sessionMetaColumnsLegacy = `session_id, agent_name, source_session_id, source_path,
  working_directory, git_branch, model, title, started_ms, ended_ms,
  turns, tools, skills, NULL, NULL, deriver_version, derived_at`

// staleTokensSchema reports the pre-0006 "tokens columns missing" condition.
func staleTokensSchema(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such column: tokens_in")
}

const upsertSessionMetaSQL = `INSERT OR REPLACE INTO session_meta(
  session_id, agent_name, source_session_id, source_path, working_directory,
  git_branch, model, title, started_ms, ended_ms, turns, tools, skills,
  tokens_in, tokens_out, deriver_version, derived_at
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

// ReplaceSessionDerivation atomically replaces a session's whole derived
// projection — its session_meta row and all its spans — in one tx. This is the
// single write path for derived data: meta and spans always describe the same
// derivation, never a torn mix of two runs.
func (s *sqliteStore) ReplaceSessionDerivation(ctx context.Context, meta *SessionMetaRow, rows []*SpanRow) error {
	if meta == nil {
		return fmt.Errorf("store: nil session meta")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: derivation tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := replaceSessionSpansTx(ctx, tx, meta.SessionID, rows); err != nil {
		return err
	}
	if meta.DerivedAt.IsZero() {
		meta.DerivedAt = time.Now().UTC()
	}
	var skills sql.NullString
	if len(meta.Skills) > 0 {
		b, merr := json.Marshal(meta.Skills)
		if merr != nil {
			return fmt.Errorf("store: marshal session skills: %w", merr)
		}
		skills = sql.NullString{String: string(b), Valid: true}
	}
	if _, err := tx.ExecContext(ctx, upsertSessionMetaSQL,
		meta.SessionID.String(), meta.AgentName,
		nullableString(meta.SourceSessionID), nullableString(meta.SourcePath),
		nullableString(meta.WorkingDir), nullableString(meta.GitBranch),
		nullableString(meta.Model), nullableString(meta.Title),
		meta.StartedMs, meta.EndedMs, meta.Turns, meta.Tools, skills,
		nullableInt64(meta.TokensIn), nullableInt64(meta.TokensOut),
		meta.DeriverVersion, meta.DerivedAt.UTC(),
	); err != nil {
		return fmt.Errorf("store: upsert session meta: %w", err)
	}
	return tx.Commit()
}

// ListSessionMeta returns unified session rows newest-first (by start time),
// or by total tokens when the filter asks for it.
func (s *sqliteStore) ListSessionMeta(ctx context.Context, f *SessionMetaFilter) ([]*SessionMetaRow, error) {
	out, err := s.listSessionMeta(ctx, f, false)
	if staleTokensSchema(err) {
		return s.listSessionMeta(ctx, f, true)
	}
	return out, err
}

func (s *sqliteStore) listSessionMeta(ctx context.Context, f *SessionMetaFilter, legacy bool) ([]*SessionMetaRow, error) {
	where, args := sessionMetaWhere(f)
	cols := sessionMetaColumns
	if legacy {
		cols = sessionMetaColumnsLegacy
	}
	q := `SELECT ` + cols + ` FROM session_meta`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	// On a legacy schema a token sort degrades to the default order — every
	// row's tokens read n/a, so there is no token order to honor.
	if f != nil && f.SortByTokens && !legacy {
		q += ` ORDER BY (tokens_in IS NULL AND tokens_out IS NULL),
		  (COALESCE(tokens_in,0)+COALESCE(tokens_out,0)) DESC, started_ms DESC`
	} else {
		q += " ORDER BY started_ms DESC"
	}
	if f != nil && f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list session meta: %w", err)
	}
	defer rows.Close()

	var out []*SessionMetaRow
	for rows.Next() {
		m, err := scanSessionMeta(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetSessionMeta returns one session's unified row, or nil when absent.
func (s *sqliteStore) GetSessionMeta(ctx context.Context, sessionID uuid.UUID) (*SessionMetaRow, error) {
	m, err := s.getSessionMeta(ctx, sessionID, sessionMetaColumns)
	if staleTokensSchema(err) {
		return s.getSessionMeta(ctx, sessionID, sessionMetaColumnsLegacy)
	}
	return m, err
}

func (s *sqliteStore) getSessionMeta(ctx context.Context, sessionID uuid.UUID, cols string) (*SessionMetaRow, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+cols+` FROM session_meta WHERE session_id = ?`,
		sessionID.String())
	m, err := scanSessionMeta(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return m, nil
}

// sessionMetaWhere builds the WHERE clauses and args for a filter.
func sessionMetaWhere(f *SessionMetaFilter) ([]string, []any) {
	var where []string
	var args []any
	if f == nil {
		return where, args
	}
	if f.Agent != "" {
		where = append(where, "agent_name = ?")
		args = append(args, f.Agent)
	}
	if len(f.Dirs) > 0 {
		where = append(where, "working_directory IN ("+placeholders(len(f.Dirs))+")")
		for _, d := range f.Dirs {
			args = append(args, d)
		}
	}
	if f.Since != nil {
		where = append(where, "started_ms >= ?")
		args = append(args, f.Since.UTC().UnixMilli())
	}
	if f.Until != nil {
		where = append(where, "started_ms <= ?")
		args = append(args, f.Until.UTC().UnixMilli())
	}
	if f.Skill != "" {
		// skills is a JSON array of names; match exact membership.
		where = append(where,
			`EXISTS (SELECT 1 FROM json_each(session_meta.skills) WHERE json_each.value = ?)`)
		args = append(args, f.Skill)
	} else if f.SkillsOnly {
		where = append(where, `json_array_length(COALESCE(skills, '[]')) > 0`)
	}
	return where, args
}

func scanSessionMeta(row interface{ Scan(...any) error }) (*SessionMetaRow, error) {
	var (
		m                              SessionMetaRow
		sid                            string
		srcSession, srcPath, wd        sql.NullString
		branch, model, title, skillsJS sql.NullString
		tokensIn, tokensOut            sql.NullInt64
		derivedAt                      time.Time
	)
	if err := row.Scan(&sid, &m.AgentName, &srcSession, &srcPath, &wd,
		&branch, &model, &title, &m.StartedMs, &m.EndedMs,
		&m.Turns, &m.Tools, &skillsJS, &tokensIn, &tokensOut,
		&m.DeriverVersion, &derivedAt); err != nil {
		return nil, fmt.Errorf("store: scan session meta: %w", err)
	}
	m.TokensIn = nullInt64Ptr(tokensIn)
	m.TokensOut = nullInt64Ptr(tokensOut)
	id, err := uuid.Parse(sid)
	if err != nil {
		return nil, fmt.Errorf("store: bad session_meta session_id %q: %w", sid, err)
	}
	m.SessionID = id
	m.SourceSessionID = srcSession.String
	m.SourcePath = srcPath.String
	m.WorkingDir = wd.String
	m.GitBranch = branch.String
	m.Model = model.String
	m.Title = title.String
	if skillsJS.Valid && skillsJS.String != "" {
		if err := json.Unmarshal([]byte(skillsJS.String), &m.Skills); err != nil {
			return nil, fmt.Errorf("store: bad session skills JSON: %w", err)
		}
	}
	m.DerivedAt = derivedAt.UTC()
	return &m, nil
}
