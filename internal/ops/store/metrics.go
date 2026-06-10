package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// This file is the read-side aggregation layer for skill observability
// ("which skill ran, did it work, what did it cost"). Everything here is
// computed in SQL over the spans table — GROUP BY over json_extract of the
// span attributes — never by looping rows in Go, so the dashboard stays cheap
// even on large capture DBs.
//
// Dotted attribute keys require the quoted JSON-path form:
// json_extract(attributes, '$."skill.name"'). skill.verified is marshaled as
// JSON true/false and extracts as integer 1/0 in SQLite, but a deriver could
// plausibly emit the string "true" — the verified expression matches both.
//
// Token attribution is session-level: a skill's token cost is the sum of
// gen_ai.usage.* over the LLM spans of every session in which the skill
// produced at least one SKILL span. SKILL spans themselves carry no usage
// data, and a loaded skill shapes every subsequent turn, so the session is
// the honest attribution unit. The deliberate consequence: a session that
// fired two skills contributes its tokens to both. Surfaces must label this
// "tokens in sessions where this skill fired", never "exclusive cost".

// MetricsFilter is the common scope for the skill aggregations. Zero fields
// are ignored. Dirs scopes by session working directory (resolved through
// raw_traces — spans carry no working_directory column); Since/Until bound
// the SKILL spans' start_ms.
type MetricsFilter struct {
	Skill string // exact skill.name match; empty = all skills
	Dirs  []string
	Since *time.Time
	Until *time.Time
}

// SQL fragments shared by every aggregation. skillVerifiedExpr yields 1/0 per
// SKILL span so SUM() counts verified invocations.
const (
	skillNameExpr     = `json_extract(attributes, '$."skill.name"')`
	skillVerifiedExpr = `CASE WHEN json_extract(attributes, '$."skill.verified"') IN (1, 'true') THEN 1 ELSE 0 END`
	llmInTokExpr      = `json_extract(attributes, '$."gen_ai.usage.input_tokens"')`
	llmOutTokExpr     = `json_extract(attributes, '$."gen_ai.usage.output_tokens"')`
)

// skillSpanWhere builds the WHERE clauses selecting the SKILL spans in scope
// (kind, non-empty skill.name, optional name/dirs/window) plus their args.
func skillSpanWhere(f *MetricsFilter) ([]string, []any) {
	clauses := []string{
		"kind = 'SKILL'",
		skillNameExpr + " IS NOT NULL",
		skillNameExpr + " != ''",
	}
	var args []any
	if f == nil {
		return clauses, args
	}
	if f.Skill != "" {
		clauses = append(clauses, skillNameExpr+" = ?")
		args = append(args, f.Skill)
	}
	if len(f.Dirs) > 0 {
		// Spans have no working_directory; scope through the session's raw rows
		// (same precedent as rawSessionWhere's skill subquery, inverted).
		clauses = append(clauses,
			"session_id IN (SELECT DISTINCT session_id FROM raw_traces WHERE working_directory IN ("+
				placeholders(len(f.Dirs))+"))")
		for _, d := range f.Dirs {
			args = append(args, d)
		}
	}
	if f.Since != nil {
		clauses = append(clauses, "start_ms >= ?")
		args = append(args, f.Since.UTC().UnixMilli())
	}
	if f.Until != nil {
		clauses = append(clauses, "start_ms <= ?")
		args = append(args, f.Until.UTC().UnixMilli())
	}
	return clauses, args
}

// skillSpanWhereSQL renders skillSpanWhere as a complete WHERE string.
func skillSpanWhereSQL(f *MetricsFilter) (string, []any) {
	clauses, args := skillSpanWhere(f)
	return "WHERE " + strings.Join(clauses, " AND "), args
}

// SkillUsage is the per-skill invocation rollup over SKILL spans.
type SkillUsage struct {
	Skill        string
	Invocations  int64 // SKILL spans
	Sessions     int64 // distinct sessions with ≥1 SKILL span
	Verified     int64 // invocations whose lock identity was load-path proven
	FirstFiredMs int64
	LastFiredMs  int64
}

// SkillUsageRollup aggregates SKILL spans per skill, most-recently-fired
// first. With f.Skill set the result has at most one element.
func (s *sqliteStore) SkillUsageRollup(ctx context.Context, f *MetricsFilter) ([]*SkillUsage, error) {
	where, args := skillSpanWhereSQL(f)
	q := `SELECT ` + skillNameExpr + ` AS skill,
	  COUNT(*),
	  COUNT(DISTINCT session_id),
	  SUM(` + skillVerifiedExpr + `),
	  MIN(start_ms),
	  MAX(start_ms)
	FROM spans ` + where + `
	GROUP BY skill
	ORDER BY MAX(start_ms) DESC, COUNT(*) DESC`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: skill usage rollup: %w", err)
	}
	defer rows.Close()

	var out []*SkillUsage
	for rows.Next() {
		u := &SkillUsage{}
		if err := rows.Scan(&u.Skill, &u.Invocations, &u.Sessions, &u.Verified,
			&u.FirstFiredMs, &u.LastFiredMs); err != nil {
			return nil, fmt.Errorf("store: scan skill usage: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// TokenTotals is the session-attributed token rollup for one skill (see the
// attribution note at the top of this file).
type TokenTotals struct {
	Sessions     int64 // sessions that contributed tokens (had ≥1 LLM span)
	InputTokens  int64
	OutputTokens int64
}

// SkillTokenRollup returns per-skill token totals over the LLM spans of each
// skill's sessions, keyed by skill name. Skills whose sessions carry no LLM
// usage data (e.g. codex-derived spans) are absent or zero — callers treat
// missing as zero.
func (s *sqliteStore) SkillTokenRollup(ctx context.Context, f *MetricsFilter) (map[string]*TokenTotals, error) {
	where, args := skillSpanWhereSQL(f)
	q := `WITH skill_sessions AS (
	  SELECT DISTINCT ` + skillNameExpr + ` AS skill, session_id
	  FROM spans ` + where + `
	)
	SELECT ss.skill,
	  COUNT(DISTINCT l.session_id),
	  CAST(COALESCE(SUM(` + strings.ReplaceAll(llmInTokExpr, "attributes", "l.attributes") + `), 0) AS INTEGER),
	  CAST(COALESCE(SUM(` + strings.ReplaceAll(llmOutTokExpr, "attributes", "l.attributes") + `), 0) AS INTEGER)
	FROM skill_sessions ss
	JOIN spans l ON l.session_id = ss.session_id AND l.kind = 'LLM'
	GROUP BY ss.skill`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: skill token rollup: %w", err)
	}
	defer rows.Close()

	out := map[string]*TokenTotals{}
	for rows.Next() {
		var skill string
		t := &TokenTotals{}
		if err := rows.Scan(&skill, &t.Sessions, &t.InputTokens, &t.OutputTokens); err != nil {
			return nil, fmt.Errorf("store: scan token totals: %w", err)
		}
		out[skill] = t
	}
	return out, rows.Err()
}

// SkillSeriesPoint is one (day, agent) bucket of a skill's invocation series.
type SkillSeriesPoint struct {
	Day         string // YYYY-MM-DD (UTC)
	Agent       string
	Invocations int64
	Verified    int64
}

// SkillInvocationSeries buckets one skill's SKILL spans by UTC day and agent,
// oldest day first. f.Skill is required.
func (s *sqliteStore) SkillInvocationSeries(ctx context.Context, f *MetricsFilter) ([]*SkillSeriesPoint, error) {
	if f == nil || f.Skill == "" {
		return nil, fmt.Errorf("store: skill invocation series: Skill is required")
	}
	where, args := skillSpanWhereSQL(f)
	q := `SELECT date(start_ms/1000, 'unixepoch') AS day, agent_name,
	  COUNT(*),
	  SUM(` + skillVerifiedExpr + `)
	FROM spans ` + where + `
	GROUP BY day, agent_name
	ORDER BY day ASC, agent_name ASC`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: skill invocation series: %w", err)
	}
	defer rows.Close()

	var out []*SkillSeriesPoint
	for rows.Next() {
		p := &SkillSeriesPoint{}
		if err := rows.Scan(&p.Day, &p.Agent, &p.Invocations, &p.Verified); err != nil {
			return nil, fmt.Errorf("store: scan series point: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SkillAgentUsage is one skill's rollup for a single agent.
type SkillAgentUsage struct {
	Agent       string
	Invocations int64
	Verified    int64
	Sessions    int64
	LastFiredMs int64
}

// SkillAgentRollup aggregates one skill's SKILL spans per agent, busiest
// agent first. f.Skill is required.
func (s *sqliteStore) SkillAgentRollup(ctx context.Context, f *MetricsFilter) ([]*SkillAgentUsage, error) {
	if f == nil || f.Skill == "" {
		return nil, fmt.Errorf("store: skill agent rollup: Skill is required")
	}
	where, args := skillSpanWhereSQL(f)
	q := `SELECT agent_name,
	  COUNT(*),
	  SUM(` + skillVerifiedExpr + `),
	  COUNT(DISTINCT session_id),
	  MAX(start_ms)
	FROM spans ` + where + `
	GROUP BY agent_name
	ORDER BY COUNT(*) DESC, agent_name ASC`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: skill agent rollup: %w", err)
	}
	defer rows.Close()

	var out []*SkillAgentUsage
	for rows.Next() {
		a := &SkillAgentUsage{}
		if err := rows.Scan(&a.Agent, &a.Invocations, &a.Verified, &a.Sessions, &a.LastFiredMs); err != nil {
			return nil, fmt.Errorf("store: scan agent usage: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SkillVersionUsage is one skill's rollup per (ref, commit) version as
// recorded on its SKILL spans — the lineage view's data: how each pinned
// version behaved while it was the installed one.
type SkillVersionUsage struct {
	Ref          string // skill.version on the span (the lock Ref at fire time)
	Commit       string // skill.commit on the span
	Invocations  int64
	Sessions     int64
	Verified     int64
	FirstFiredMs int64
	LastFiredMs  int64
	InputTokens  int64 // session-attributed, keyed by this version's session set
	OutputTokens int64
}

// SkillVersionRollup groups one skill's SKILL spans by the version identity
// they carried, newest-first by first-fired time. f.Skill is required.
func (s *sqliteStore) SkillVersionRollup(ctx context.Context, f *MetricsFilter) ([]*SkillVersionUsage, error) {
	if f == nil || f.Skill == "" {
		return nil, fmt.Errorf("store: skill version rollup: Skill is required")
	}
	where, args := skillSpanWhereSQL(f)
	q := `WITH ver AS (
	  SELECT COALESCE(json_extract(attributes, '$."skill.version"'), '') AS ref,
	         COALESCE(json_extract(attributes, '$."skill.commit"'), '')  AS commit_sha,
	         session_id, start_ms,
	         ` + skillVerifiedExpr + ` AS v
	  FROM spans ` + where + `
	),
	tok AS (
	  SELECT vs.ref, vs.commit_sha,
	    CAST(COALESCE(SUM(` + strings.ReplaceAll(llmInTokExpr, "attributes", "l.attributes") + `), 0) AS INTEGER) AS tin,
	    CAST(COALESCE(SUM(` + strings.ReplaceAll(llmOutTokExpr, "attributes", "l.attributes") + `), 0) AS INTEGER) AS tout
	  FROM (SELECT DISTINCT ref, commit_sha, session_id FROM ver) vs
	  JOIN spans l ON l.session_id = vs.session_id AND l.kind = 'LLM'
	  GROUP BY vs.ref, vs.commit_sha
	)
	SELECT ver.ref, ver.commit_sha,
	  COUNT(*),
	  COUNT(DISTINCT ver.session_id),
	  SUM(ver.v),
	  MIN(ver.start_ms),
	  MAX(ver.start_ms),
	  COALESCE(MAX(tok.tin), 0),
	  COALESCE(MAX(tok.tout), 0)
	FROM ver
	LEFT JOIN tok ON tok.ref = ver.ref AND tok.commit_sha = ver.commit_sha
	GROUP BY ver.ref, ver.commit_sha
	ORDER BY MIN(ver.start_ms) DESC`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: skill version rollup: %w", err)
	}
	defer rows.Close()

	var out []*SkillVersionUsage
	for rows.Next() {
		v := &SkillVersionUsage{}
		if err := rows.Scan(&v.Ref, &v.Commit, &v.Invocations, &v.Sessions, &v.Verified,
			&v.FirstFiredMs, &v.LastFiredMs, &v.InputTokens, &v.OutputTokens); err != nil {
			return nil, fmt.Errorf("store: scan version usage: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
