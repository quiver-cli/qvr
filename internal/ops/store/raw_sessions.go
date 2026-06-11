package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/astra-sh/qvr/internal/ops"
	"github.com/google/uuid"
)

// RawSession is a per-session summary computed on the fly from raw_traces.
// There is no sessions table in the raw-only model — a "session" is just the
// set of rows sharing a session_id, so its boundaries and counts are derived.
type RawSession struct {
	SessionID uuid.UUID `json:"session_id"`
	// Title is the first prompt the user typed, derived on demand by the UI
	// layer (not stored). It's the human-readable name of the session; empty
	// until populated by a caller that has the session's raw rows.
	Title            string    `json:"title,omitempty"`
	AgentName        string    `json:"agent_name"`
	AgentSessionID   string    `json:"agent_session_id,omitempty"`
	WorkingDirectory string    `json:"working_directory,omitempty"`
	StartedAt        time.Time `json:"started_at"`
	LastAt           time.Time `json:"last_at"`
	TranscriptLines  int64     `json:"transcript_lines"`
	HookPayloads     int64     `json:"hook_payloads"`
	TotalRows        int64     `json:"total_rows"`
	// Skills lists the distinct skills this session used (skill.name from its
	// SKILL-attributed spans). Derived on demand by a caller that has the store;
	// empty until populated. Drives the skill filter + chips on the UI.
	Skills []string `json:"skills,omitempty"`
}

// RawSessionFilter scopes ListRawSessions. Nil/zero fields are ignored.
type RawSessionFilter struct {
	Since *time.Time // sessions whose first row is at/after this time
	Until *time.Time // sessions whose first row is at/before this time
	Agent string
	Skill string // only sessions that used this skill (matched via spans)
	// SkillsOnly restricts the result to sessions that used at least one skill
	// (any skill-attributed span). The DB still stores every session — capture's
	// retention gate only prunes settled skill-less sessions, so sessions that
	// haven't ended, lack a deriver, or were explicitly ingested can still be
	// skill-less. This is the read-side surfacing filter the UI sets so those
	// never appear in the dashboard. Ignored when Skill is set (a specific skill
	// already implies skill-bearing).
	SkillsOnly bool
	Dirs       []string // working_directory ∈ Dirs (empty = all)
	Limit      int
}

const rawSessionSelect = `SELECT
  session_id,
  MAX(agent_name),
  MAX(COALESCE(agent_session_id,'')),
  MAX(COALESCE(working_directory,'')),
  MIN(captured_at),
  MAX(captured_at),
  SUM(CASE WHEN source='transcript'   THEN 1 ELSE 0 END),
  SUM(CASE WHEN source='hook_payload' THEN 1 ELSE 0 END),
  COUNT(*)
FROM raw_traces`

// ListRawSessions returns session summaries newest-first (by first-seen time).
func (s *sqliteStore) ListRawSessions(ctx context.Context, f *RawSessionFilter) ([]*RawSession, error) {
	q, args := buildRawSessionQuery(f)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list raw sessions: %w", err)
	}
	defer rows.Close()

	var out []*RawSession
	for rows.Next() {
		rs, err := scanRawSession(rows)
		if err != nil {
			return nil, err
		}
		if rs == nil {
			continue // skip a non-uuid session key rather than failing the list
		}
		out = append(out, rs)
	}
	return out, rows.Err()
}

// buildRawSessionQuery assembles the aggregate query (WHERE/GROUP BY/HAVING/
// ORDER BY/LIMIT) and its argument list for the given filter.
func buildRawSessionQuery(f *RawSessionFilter) (string, []any) {
	where, args := rawSessionWhere(f)
	q := rawSessionSelect
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " GROUP BY session_id"
	having := rawSessionHaving(f, &args)
	if len(having) > 0 {
		q += " HAVING " + strings.Join(having, " AND ")
	}
	q += " ORDER BY MIN(captured_at) DESC"
	if f != nil && f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}
	return q, args
}

// rawSessionWhere builds the row-level WHERE clauses (agent/dirs/skill scoping)
// and the matching positional args.
func rawSessionWhere(f *RawSessionFilter) ([]string, []any) {
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
	if f.Skill != "" {
		// A session "used" a skill when one of its derived spans is attributed
		// to it (skill.name lives inside the spans.attributes JSON). Restrict
		// to sessions whose span set includes the requested skill.
		where = append(where,
			`session_id IN (SELECT DISTINCT session_id FROM spans
			  WHERE json_extract(attributes, '$."skill.name"') = ?)`)
		args = append(args, f.Skill)
	} else if f.SkillsOnly {
		// Surface only skill-bearing sessions: keep those with at least one
		// span attributed to any skill. The DB still holds skill-less sessions
		// (see SkillsOnly doc) — this hides them from the listing without
		// deleting them. Redundant when f.Skill is set, hence the else.
		where = append(where,
			`session_id IN (SELECT DISTINCT session_id FROM spans
			  WHERE json_extract(attributes, '$."skill.name"') IS NOT NULL
			    AND json_extract(attributes, '$."skill.name"') != '')`)
	}
	return where, args
}

// rawSessionHaving builds the post-aggregation HAVING clauses (since/until on
// first-seen time), appending their args to *args in clause order.
func rawSessionHaving(f *RawSessionFilter, args *[]any) []string {
	var having []string
	if f != nil && f.Since != nil {
		having = append(having, "MIN(captured_at) >= ?")
		*args = append(*args, f.Since.UTC())
	}
	if f != nil && f.Until != nil {
		having = append(having, "MIN(captured_at) <= ?")
		*args = append(*args, f.Until.UTC())
	}
	return having
}

// scanRawSession reads one aggregate row into a RawSession. It returns
// (nil, nil) for a row whose session key isn't a UUID so the caller can skip it
// without failing the whole listing.
func scanRawSession(rows *sql.Rows) (*RawSession, error) {
	var (
		sid, agent, agentSID, wd string
		startedStr, lastStr      string
		rs                       RawSession
	)
	if err := rows.Scan(&sid, &agent, &agentSID, &wd, &startedStr, &lastStr,
		&rs.TranscriptLines, &rs.HookPayloads, &rs.TotalRows); err != nil {
		return nil, fmt.Errorf("store: scan raw session: %w", err)
	}
	id, err := uuid.Parse(sid)
	if err != nil {
		return nil, nil // skip a non-uuid session key rather than failing the list
	}
	rs.SessionID = id
	rs.AgentName = agent
	rs.AgentSessionID = agentSID
	rs.WorkingDirectory = wd
	if t, err := parseSQLiteTime(startedStr); err == nil {
		rs.StartedAt = t
	}
	if t, err := parseSQLiteTime(lastStr); err == nil {
		rs.LastAt = t
	}
	return &rs, nil
}

// SkillsForSessions returns the distinct skill names attributed to each given
// session, keyed by session id string. skill.name lives inside each span's
// attributes JSON, so this extracts it from the spans of the requested
// sessions. Sessions with no skill span are absent from the result. An empty
// ids slice returns an empty map without querying.
func (s *sqliteStore) SkillsForSessions(ctx context.Context, ids []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	q := `SELECT DISTINCT session_id, json_extract(attributes, '$."skill.name"') AS skill
	      FROM spans
	      WHERE session_id IN (` + placeholders(len(ids)) + `)
	        AND skill IS NOT NULL AND skill != ''
	      ORDER BY session_id, skill`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: skills for sessions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sid, skill string
		if err := rows.Scan(&sid, &skill); err != nil {
			return nil, fmt.Errorf("store: scan session skill: %w", err)
		}
		out[sid] = append(out[sid], skill)
	}
	return out, rows.Err()
}

// CountRawSessions counts distinct sessions, optionally scoped to dirs/agent.
func (s *sqliteStore) CountRawSessions(ctx context.Context, dirs []string, agent string) (int64, error) {
	where, args := rawScopeWhere(dirs, agent)
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT session_id) FROM raw_traces `+where, args...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count raw sessions: %w", err)
	}
	return n, nil
}

// CountRawTraces counts rows, optionally scoped to dirs/agent.
func (s *sqliteStore) CountRawTraces(ctx context.Context, dirs []string, agent string) (int64, error) {
	where, args := rawScopeWhere(dirs, agent)
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM raw_traces `+where, args...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count raw traces: %w", err)
	}
	return n, nil
}

// DistinctRawAgents returns every agent name present in raw_traces, sorted.
func (s *sqliteStore) DistinctRawAgents(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT agent_name FROM raw_traces ORDER BY agent_name`)
	if err != nil {
		return nil, fmt.Errorf("store: distinct raw agents: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("store: distinct raw agents: %w", err)
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// LatestRawAt returns the newest capture time for an agent, or nil if none.
func (s *sqliteStore) LatestRawAt(ctx context.Context, agent string) (*time.Time, error) {
	where, args := rawScopeWhere(nil, agent)
	var raw any
	err := s.db.QueryRowContext(ctx,
		`SELECT MAX(captured_at) FROM raw_traces `+where, args...).Scan(&raw)
	if err != nil {
		return nil, fmt.Errorf("store: latest raw: %w", err)
	}
	switch v := raw.(type) {
	case time.Time:
		t := v.UTC()
		return &t, nil
	case string:
		if t, err := parseSQLiteTime(v); err == nil {
			return &t, nil
		}
	}
	return nil, nil
}

// rawScopeWhere builds an optional WHERE for dirs/agent scoping.
func rawScopeWhere(dirs []string, agent string) (string, []any) {
	var clauses []string
	var args []any
	if agent != "" {
		clauses = append(clauses, "agent_name = ?")
		args = append(args, agent)
	}
	if len(dirs) > 0 {
		clauses = append(clauses, "working_directory IN ("+placeholders(len(dirs))+")")
		for _, d := range dirs {
			args = append(args, d)
		}
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

// StreamRawTraces calls fn for each row matching f, ordered by (session_id,
// seq). Used by export. Returns on the first non-nil fn error.
func (s *sqliteStore) StreamRawTraces(ctx context.Context, f *RawTraceFilter, fn func(*ops.RawTrace) error) error {
	where, args := f.build()
	q := `SELECT ` + rawTraceColumns + ` FROM raw_traces ` + where +
		` ORDER BY session_id ASC, seq ASC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("store: stream raw traces: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanRawTrace(rows)
		if err != nil {
			return err
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	return rows.Err()
}
