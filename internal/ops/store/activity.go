package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Activity aggregations power the dashboard's analytics overview. Everything
// reads the session_meta projection (never raw transcripts), so the queries
// stay cheap: the read model is one row per session. Should session volume
// ever outgrow live GROUP BYs, the escape hatch is a per-day rollup table
// derived from session_meta — the read API here would not change.

// ActivityFilter scopes the activity aggregations. Nil/zero fields are ignored.
type ActivityFilter struct {
	Since  *time.Time
	Until  *time.Time
	Agents []string
	Dirs   []string // working_directory ∈ Dirs (project scope); empty = all
}

// ActivityBucket is one (day, agent) cell of the sessions-over-time series.
type ActivityBucket struct {
	Day           string `json:"day"` // YYYY-MM-DD (UTC)
	Agent         string `json:"agent"`
	Sessions      int64  `json:"sessions"`
	SkillSessions int64  `json:"skill_sessions"` // sessions that used ≥1 skill
	Turns         int64  `json:"turns"`
	DurationMs    int64  `json:"duration_ms"`
}

// AgentActivity is one agent's slice of the by-agent breakdown.
type AgentActivity struct {
	Agent         string `json:"agent"`
	Sessions      int64  `json:"sessions"`
	SkillSessions int64  `json:"skill_sessions"` // sessions that used ≥1 skill
	Turns         int64  `json:"turns"`
	Tools         int64  `json:"tools"`
	DurationMs    int64  `json:"duration_ms"`
	AvgSessionMs  int64  `json:"avg_session_ms"`
}

// ActivitySummary is the analytics headline: totals plus the per-agent slices.
// Token totals come from the LLM spans of the scoped sessions (real usage, no
// per-skill double counting).
type ActivitySummary struct {
	Sessions      int64            `json:"sessions"`
	SkillSessions int64            `json:"skill_sessions"`
	Turns         int64            `json:"turns"`
	Tools         int64            `json:"tools"`
	DurationMs    int64            `json:"duration_ms"`
	TokensIn      int64            `json:"tokens_in"`
	TokensOut     int64            `json:"tokens_out"`
	Agents        []*AgentActivity `json:"agents"`
}

// SkippedBucket is one (day, agent) count of sessions the discovery scan
// proved skill-less and did not store — counted from the scan ledger, so the
// skill-vs-no-skill split never requires storing skill-less transcripts.
type SkippedBucket struct {
	Day      string `json:"day"` // YYYY-MM-DD (UTC, file mtime)
	Agent    string `json:"agent"`
	Sessions int64  `json:"sessions"`
}

// activityWhere builds the WHERE clauses + args for session_meta scoping.
// started_ms drives the time window (matching the series bucketing).
func activityWhere(f *ActivityFilter) (string, []any) {
	var where []string
	var args []any
	if f != nil {
		if len(f.Agents) > 0 {
			where = append(where, "agent_name IN ("+placeholders(len(f.Agents))+")")
			for _, a := range f.Agents {
				args = append(args, a)
			}
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
	}
	if len(where) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(where, " AND "), args
}

// ActivitySummary aggregates the headline totals and per-agent slices.
func (s *sqliteStore) ActivitySummary(ctx context.Context, f *ActivityFilter) (*ActivitySummary, error) {
	where, args := activityWhere(f)
	q := `SELECT agent_name,
	  COUNT(*),
	  SUM(CASE WHEN json_array_length(COALESCE(skills,'[]')) > 0 THEN 1 ELSE 0 END),
	  SUM(turns), SUM(tools),
	  SUM(MAX(ended_ms - started_ms, 0))
	FROM session_meta` + where + ` GROUP BY agent_name ORDER BY COUNT(*) DESC`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: activity summary: %w", err)
	}
	defer rows.Close()

	out := &ActivitySummary{Agents: []*AgentActivity{}}
	for rows.Next() {
		var a AgentActivity
		if err := rows.Scan(&a.Agent, &a.Sessions, &a.SkillSessions, &a.Turns, &a.Tools, &a.DurationMs); err != nil {
			return nil, fmt.Errorf("store: activity summary scan: %w", err)
		}
		if a.Sessions > 0 {
			a.AvgSessionMs = a.DurationMs / a.Sessions
		}
		out.Agents = append(out.Agents, &a)
		out.Sessions += a.Sessions
		out.SkillSessions += a.SkillSessions
		out.Turns += a.Turns
		out.Tools += a.Tools
		out.DurationMs += a.DurationMs
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.fillActivityTokens(ctx, f, out); err != nil {
		return nil, err
	}
	return out, nil
}

// fillActivityTokens sums real token usage over the scoped sessions' LLM
// spans (joined through session_meta so project scoping applies).
func (s *sqliteStore) fillActivityTokens(ctx context.Context, f *ActivityFilter, out *ActivitySummary) error {
	var where []string
	var args []any
	if f != nil {
		if len(f.Agents) > 0 {
			where = append(where, "m.agent_name IN ("+placeholders(len(f.Agents))+")")
			for _, a := range f.Agents {
				args = append(args, a)
			}
		}
		if len(f.Dirs) > 0 {
			where = append(where, "m.working_directory IN ("+placeholders(len(f.Dirs))+")")
			for _, d := range f.Dirs {
				args = append(args, d)
			}
		}
		if f.Since != nil {
			where = append(where, "m.started_ms >= ?")
			args = append(args, f.Since.UTC().UnixMilli())
		}
		if f.Until != nil {
			where = append(where, "m.started_ms <= ?")
			args = append(args, f.Until.UTC().UnixMilli())
		}
	}
	q := `SELECT
	  COALESCE(SUM(CAST(json_extract(sp.attributes, '$."gen_ai.usage.input_tokens"') AS INTEGER)), 0),
	  COALESCE(SUM(CAST(json_extract(sp.attributes, '$."gen_ai.usage.output_tokens"') AS INTEGER)), 0)
	FROM spans sp JOIN session_meta m ON m.session_id = sp.session_id
	WHERE sp.kind = 'LLM'`
	if len(where) > 0 {
		q += " AND " + strings.Join(where, " AND ")
	}
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&out.TokensIn, &out.TokensOut); err != nil {
		return fmt.Errorf("store: activity tokens: %w", err)
	}
	return nil
}

// ActivitySeries returns the per-day per-agent buckets, oldest day first.
// Days are UTC. Sessions land on their start day (multi-day sessions are not
// split — coding-agent sessions are overwhelmingly intra-day).
func (s *sqliteStore) ActivitySeries(ctx context.Context, f *ActivityFilter) ([]*ActivityBucket, error) {
	where, args := activityWhere(f)
	q := `SELECT date(started_ms/1000, 'unixepoch') AS day, agent_name,
	  COUNT(*),
	  SUM(CASE WHEN json_array_length(COALESCE(skills,'[]')) > 0 THEN 1 ELSE 0 END),
	  SUM(turns),
	  SUM(MAX(ended_ms - started_ms, 0))
	FROM session_meta` + where + ` GROUP BY day, agent_name ORDER BY day ASC`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: activity series: %w", err)
	}
	defer rows.Close()

	var out []*ActivityBucket
	for rows.Next() {
		var b ActivityBucket
		if err := rows.Scan(&b.Day, &b.Agent, &b.Sessions, &b.SkillSessions, &b.Turns, &b.DurationMs); err != nil {
			return nil, fmt.Errorf("store: activity series scan: %w", err)
		}
		out = append(out, &b)
	}
	return out, rows.Err()
}

// SkippedSkilllessSeries counts scan-skipped (skill-less, unstored) sessions
// per (day, agent) from the ledger, by file mtime. The ledger has no project
// scoping — these counts are machine-global by nature.
func (s *sqliteStore) SkippedSkilllessSeries(ctx context.Context, since, until *time.Time) ([]*SkippedBucket, error) {
	var where []string
	args := []any{ScanStatusSkipped}
	where = append(where, "status = ?")
	if since != nil {
		where = append(where, "mtime_ms >= ?")
		args = append(args, since.UTC().UnixMilli())
	}
	if until != nil {
		where = append(where, "mtime_ms <= ?")
		args = append(args, until.UTC().UnixMilli())
	}
	q := `SELECT date(mtime_ms/1000, 'unixepoch') AS day, agent_name, COUNT(*)
	FROM scanned_files WHERE ` + strings.Join(where, " AND ") + `
	GROUP BY day, agent_name ORDER BY day ASC`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: skipped series: %w", err)
	}
	defer rows.Close()

	var out []*SkippedBucket
	for rows.Next() {
		var b SkippedBucket
		if err := rows.Scan(&b.Day, &b.Agent, &b.Sessions); err != nil {
			return nil, fmt.Errorf("store: skipped series scan: %w", err)
		}
		out = append(out, &b)
	}
	return out, rows.Err()
}
