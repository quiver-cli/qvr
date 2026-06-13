package store

import (
	"context"
	"database/sql"
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
// json_extract(attributes, '$."skill.name"'). Version identity is
// proof-gated: enrichment stamps skill.version (and the other identity
// fields) only when the span's load path proved which locked artifact ran.
// The aggregations surface the OBSERVED VERSIONS per cut; an invocation
// without one simply has an unknown version ("" — surfaces render it as
// such). Skill attribution (the name tag) is never gated on identity:
// every SKILL span counts, version known or not.
//
// Token attribution is session-level: a skill's token cost is the sum of the
// session totals (session_meta.tokens_in/tokens_out — the derive-time sums of
// the LLM spans' gen_ai.usage.*, or the store's own session-level totals for
// agents that report usage only there, e.g. hermes and copilot's input side)
// over every session in which the skill produced at least one SKILL span.
// SKILL spans themselves carry no usage data, and a loaded skill shapes every
// subsequent turn, so the session is the honest attribution unit. The
// deliberate consequence: a session that fired two skills contributes its
// tokens to both. Surfaces must label this "tokens in sessions where this
// skill fired", never "exclusive cost".
//
// Token sums are NULL-preserving: derivers set session totals only when the
// native store reported usage, and SUM over all-NULL is NULL — so a cut whose
// sessions carry no usage data surfaces as nil (rendered n/a), never a
// fabricated 0 that would poison cross-agent comparisons. The two sides are
// independent: copilot reports per-turn output but session-level input.

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

// SQL fragments shared by every aggregation. skillVersionExpr yields the
// span's proven version ref, or "" when identity is unknown; skillVersionsAgg
// collapses a group to its known versions, NULL when none. The separator is
// the unit separator (0x1f) — a control char git forbids in ref names, unlike
// the default comma, which is legal in branch names. SQLite rejects DISTINCT
// alongside a custom separator, so splitVersions dedupes on the Go side.
const (
	versionsSep      = "\x1f"
	skillNameExpr    = `json_extract(attributes, '$."skill.name"')`
	skillVersionExpr = `COALESCE(json_extract(attributes, '$."skill.version"'), '')`
	skillVersionsAgg = `GROUP_CONCAT(NULLIF(` + skillVersionExpr + `, ''), char(31))`
	// Every token rollup joins session_meta aliased m for the per-session
	// totals. tokenSessionsAgg counts the sessions that actually reported
	// usage (either side), the honest denominator for a token cut.
	tokenSessionsAgg = `COUNT(DISTINCT CASE WHEN m.tokens_in IS NOT NULL OR m.tokens_out IS NOT NULL THEN m.session_id END)`
	// durationLatencyAgg aggregates SKILL-span self-time (end_ms - start_ms)
	// over the spans in scope, counting only MEASURED spans (end_ms > start_ms)
	// so an unfinished or point span never fabricates a 0-latency sample. It
	// emits five columns in order: measured-count, avg, min, max, total (all ms).
	// The four stat columns read 0 when nothing was measured — Measured is the
	// honest denominator the surface checks before rendering anything but n/a.
	durationLatencyAgg = `COUNT(CASE WHEN end_ms > start_ms THEN 1 END),
	  COALESCE(CAST(AVG(CASE WHEN end_ms > start_ms THEN end_ms - start_ms END) AS INTEGER), 0),
	  COALESCE(MIN(CASE WHEN end_ms > start_ms THEN end_ms - start_ms END), 0),
	  COALESCE(MAX(CASE WHEN end_ms > start_ms THEN end_ms - start_ms END), 0),
	  COALESCE(SUM(CASE WHEN end_ms > start_ms THEN end_ms - start_ms END), 0)`
)

// DurationStats is a latency/duration rollup in milliseconds. Measured is the
// count of samples that actually had a positive duration (the honest
// denominator); the Avg/Min/Max/Total fields are meaningful only when
// Measured > 0, and are 0 otherwise so the surface renders n/a rather than a
// fabricated zero.
type DurationStats struct {
	Measured int64
	AvgMs    int64
	MinMs    int64
	MaxMs    int64
	TotalMs  int64
}

// legacyTokenQuery rewrites a token rollup for a pre-0006 schema (read-only
// open, migration not applied): the meta token columns don't exist yet, so
// they read as NULL (n/a) instead of failing the whole aggregation. It assumes
// every token rollup aliases session_meta as `m` (the convention all queries
// here follow) — a query that joins it under a different alias would not be
// rewritten and would surface the raw column-not-found error instead.
func legacyTokenQuery(q string) string {
	q = strings.ReplaceAll(q, "m.tokens_in", "NULL")
	return strings.ReplaceAll(q, "m.tokens_out", "NULL")
}

// queryTokenRollup runs a token rollup query, degrading to the NULL-token
// form on a pre-0006 schema.
func (s *sqliteStore) queryTokenRollup(ctx context.Context, q string, args []any) (*sql.Rows, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if staleTokensSchema(err) {
		rows, err = s.db.QueryContext(ctx, legacyTokenQuery(q), args...)
	}
	return rows, err
}

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
	Invocations  int64    // SKILL spans
	Sessions     int64    // distinct sessions with ≥1 SKILL span
	Versions     []string // distinct proven versions observed; empty = unknown
	FirstFiredMs int64
	LastFiredMs  int64
	// Latency is the exclusive self-time of this skill's SKILL spans — how long
	// the skill's own tool call ran (small for name-only loads, real for script
	// skills). Measured == 0 means no span had a positive duration (n/a).
	Latency DurationStats
}

// SkillUsageRollup aggregates SKILL spans per skill, most-recently-fired
// first. With f.Skill set the result has at most one element.
func (s *sqliteStore) SkillUsageRollup(ctx context.Context, f *MetricsFilter) ([]*SkillUsage, error) {
	where, args := skillSpanWhereSQL(f)
	q := `SELECT ` + skillNameExpr + ` AS skill,
	  COUNT(*),
	  COUNT(DISTINCT session_id),
	  ` + skillVersionsAgg + `,
	  MIN(start_ms),
	  MAX(start_ms),
	  ` + durationLatencyAgg + `
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
		var versions sql.NullString
		if err := rows.Scan(&u.Skill, &u.Invocations, &u.Sessions, &versions,
			&u.FirstFiredMs, &u.LastFiredMs,
			&u.Latency.Measured, &u.Latency.AvgMs, &u.Latency.MinMs,
			&u.Latency.MaxMs, &u.Latency.TotalMs); err != nil {
			return nil, fmt.Errorf("store: scan skill usage: %w", err)
		}
		u.Versions = splitVersions(versions)
		out = append(out, u)
	}
	return out, rows.Err()
}

// splitVersions decodes a skillVersionsAgg column: NULL (no proven version in
// the group) → nil, else the joined refs deduped in first-seen order (the
// aggregate cannot use DISTINCT alongside its custom separator).
func splitVersions(v sql.NullString) []string {
	if !v.Valid || v.String == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for ref := range strings.SplitSeq(v.String, versionsSep) {
		if !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	return out
}

// TokenTotals is the session-attributed token rollup for one skill (see the
// attribution note at the top of this file). nil token sides mean the cut's
// sessions reported no usage there (n/a) — distinct from a genuine 0.
type TokenTotals struct {
	Sessions     int64 // sessions that reported usage on either side
	InputTokens  *int64
	OutputTokens *int64
}

// SkillTokenRollup returns per-skill token totals over the session totals of
// each skill's sessions, keyed by skill name. A skill whose sessions carry no
// usage data (token-less agents) maps to nil token sides, never 0.
func (s *sqliteStore) SkillTokenRollup(ctx context.Context, f *MetricsFilter) (map[string]*TokenTotals, error) {
	where, args := skillSpanWhereSQL(f)
	q := `WITH skill_sessions AS (
	  SELECT DISTINCT ` + skillNameExpr + ` AS skill, session_id
	  FROM spans ` + where + `
	)
	SELECT ss.skill,
	  ` + tokenSessionsAgg + `,
	  CAST(SUM(m.tokens_in) AS INTEGER),
	  CAST(SUM(m.tokens_out) AS INTEGER)
	FROM skill_sessions ss
	JOIN session_meta m ON m.session_id = ss.session_id
	GROUP BY ss.skill`

	rows, err := s.queryTokenRollup(ctx, q, args)
	if err != nil {
		return nil, fmt.Errorf("store: skill token rollup: %w", err)
	}
	defer rows.Close()

	out := map[string]*TokenTotals{}
	for rows.Next() {
		var skill string
		var tin, tout sql.NullInt64
		t := &TokenTotals{}
		if err := rows.Scan(&skill, &t.Sessions, &tin, &tout); err != nil {
			return nil, fmt.Errorf("store: scan token totals: %w", err)
		}
		t.InputTokens = nullInt64Ptr(tin)
		t.OutputTokens = nullInt64Ptr(tout)
		out[skill] = t
	}
	return out, rows.Err()
}

// SkillSessionDurationRollup returns per-skill session wall-clock duration: the
// (ended_ms - started_ms) of each session the skill fired in, aggregated as a
// DurationStats keyed by skill name. Session-attributed like the token rollup —
// a session that fired two skills contributes its whole wall-clock to both
// (exposure, not exclusive). Sessions with no positive duration (ended<=started,
// e.g. a single-event session) are excluded from the stats; Measured is the
// count that contributed. Session times are base columns (present pre-0006), so
// no schema-degrade path is needed.
func (s *sqliteStore) SkillSessionDurationRollup(ctx context.Context, f *MetricsFilter) (map[string]*DurationStats, error) {
	where, args := skillSpanWhereSQL(f)
	q := `WITH skill_sessions AS (
	  SELECT DISTINCT ` + skillNameExpr + ` AS skill, session_id
	  FROM spans ` + where + `
	)
	SELECT ss.skill,
	  COUNT(CASE WHEN m.ended_ms > m.started_ms THEN 1 END),
	  COALESCE(CAST(AVG(CASE WHEN m.ended_ms > m.started_ms THEN m.ended_ms - m.started_ms END) AS INTEGER), 0),
	  COALESCE(MIN(CASE WHEN m.ended_ms > m.started_ms THEN m.ended_ms - m.started_ms END), 0),
	  COALESCE(MAX(CASE WHEN m.ended_ms > m.started_ms THEN m.ended_ms - m.started_ms END), 0),
	  COALESCE(SUM(CASE WHEN m.ended_ms > m.started_ms THEN m.ended_ms - m.started_ms END), 0)
	FROM skill_sessions ss
	JOIN session_meta m ON m.session_id = ss.session_id
	GROUP BY ss.skill`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: skill session duration rollup: %w", err)
	}
	defer rows.Close()

	out := map[string]*DurationStats{}
	for rows.Next() {
		var skill string
		d := &DurationStats{}
		if err := rows.Scan(&skill, &d.Measured, &d.AvgMs, &d.MinMs, &d.MaxMs, &d.TotalMs); err != nil {
			return nil, fmt.Errorf("store: scan session duration: %w", err)
		}
		out[skill] = d
	}
	return out, rows.Err()
}

// SkillSeriesPoint is one (day, agent) bucket of a skill's invocation series.
type SkillSeriesPoint struct {
	Day         string // YYYY-MM-DD (UTC)
	Agent       string
	Invocations int64
}

// SkillInvocationSeries buckets one skill's SKILL spans by UTC day and agent,
// oldest day first. f.Skill is required.
func (s *sqliteStore) SkillInvocationSeries(ctx context.Context, f *MetricsFilter) ([]*SkillSeriesPoint, error) {
	if f == nil || f.Skill == "" {
		return nil, fmt.Errorf("store: skill invocation series: Skill is required")
	}
	where, args := skillSpanWhereSQL(f)
	q := `SELECT date(start_ms/1000, 'unixepoch') AS day, agent_name,
	  COUNT(*)
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
		if err := rows.Scan(&p.Day, &p.Agent, &p.Invocations); err != nil {
			return nil, fmt.Errorf("store: scan series point: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SkillAgentUsage is one skill's rollup for a single agent. Versions is the
// distinct proven versions this agent's invocations carried — empty when the
// agent's evidence never pinned one (surfaces render "unknown"). Tokens are
// session-attributed within the cut (the tokens of this agent's sessions
// where the skill fired); nil sides mean no usage reported (n/a).
type SkillAgentUsage struct {
	Agent         string
	Invocations   int64
	Versions      []string
	Sessions      int64
	LastFiredMs   int64
	InputTokens   *int64
	OutputTokens  *int64
	TokenSessions int64 // sessions in the cut that reported usage
}

// SkillAgentRollup aggregates one skill's SKILL spans per agent, busiest
// agent first. f.Skill is required. An agent partitions the sessions (a
// session has one agent), so per-agent tokens sum to the skill aggregate.
func (s *sqliteStore) SkillAgentRollup(ctx context.Context, f *MetricsFilter) ([]*SkillAgentUsage, error) {
	if f == nil || f.Skill == "" {
		return nil, fmt.Errorf("store: skill agent rollup: Skill is required")
	}
	where, args := skillSpanWhereSQL(f)
	q := `WITH sk AS (
	  SELECT agent_name, session_id, start_ms, attributes
	  FROM spans ` + where + `
	),
	tok AS (
	  SELECT ss.agent_name,
	    CAST(SUM(m.tokens_in) AS INTEGER)  AS tin,
	    CAST(SUM(m.tokens_out) AS INTEGER) AS tout,
	    ` + tokenSessionsAgg + ` AS tses
	  FROM (SELECT DISTINCT agent_name, session_id FROM sk) ss
	  JOIN session_meta m ON m.session_id = ss.session_id
	  GROUP BY ss.agent_name
	)
	SELECT sk.agent_name,
	  COUNT(*),
	  ` + skillVersionsAgg + `,
	  COUNT(DISTINCT sk.session_id),
	  MAX(sk.start_ms),
	  -- tok emits exactly one row per agent_name (it GROUPs BY ss.agent_name), so
	  -- MAX(t.*) just lifts that single value through this GROUP BY — it is not a
	  -- real aggregate. If tok is ever changed to emit multiple rows per key, this
	  -- must become SUM.
	  MAX(t.tin), MAX(t.tout), COALESCE(MAX(t.tses), 0)
	FROM sk LEFT JOIN tok t ON t.agent_name = sk.agent_name
	GROUP BY sk.agent_name
	ORDER BY COUNT(*) DESC, sk.agent_name ASC`

	rows, err := s.queryTokenRollup(ctx, q, args)
	if err != nil {
		return nil, fmt.Errorf("store: skill agent rollup: %w", err)
	}
	defer rows.Close()

	var out []*SkillAgentUsage
	for rows.Next() {
		a := &SkillAgentUsage{}
		var versions sql.NullString
		var tin, tout sql.NullInt64
		if err := rows.Scan(&a.Agent, &a.Invocations, &versions, &a.Sessions,
			&a.LastFiredMs, &tin, &tout, &a.TokenSessions); err != nil {
			return nil, fmt.Errorf("store: scan agent usage: %w", err)
		}
		a.Versions = splitVersions(versions)
		a.InputTokens = nullInt64Ptr(tin)
		a.OutputTokens = nullInt64Ptr(tout)
		out = append(out, a)
	}
	return out, rows.Err()
}

// SkillModelUsage is one skill's rollup for a single model — the
// "skill A on opus vs skill A on fable" cut. Model is the turn's
// gen_ai.request.model stamped onto the skill's spans at derive time;
// "" groups spans whose transcript carried no model. Tokens are
// session-attributed within the cut; nil sides mean no usage reported (n/a).
type SkillModelUsage struct {
	Model         string
	Invocations   int64
	Sessions      int64
	LastFiredMs   int64
	InputTokens   *int64
	OutputTokens  *int64
	TokenSessions int64 // sessions in the cut that reported usage
}

// SkillModelRollup aggregates one skill's SKILL spans per model, busiest
// model first. f.Skill is required. Unlike the agent cut, models OVERLAP: a
// session that invoked the skill on two models contributes its whole session
// tokens to both rows — exposure, not exclusive cost. Surfaces must label it
// that way.
func (s *sqliteStore) SkillModelRollup(ctx context.Context, f *MetricsFilter) ([]*SkillModelUsage, error) {
	if f == nil || f.Skill == "" {
		return nil, fmt.Errorf("store: skill model rollup: Skill is required")
	}
	where, args := skillSpanWhereSQL(f)
	q := `WITH sk AS (
	  SELECT COALESCE(json_extract(attributes, '$."gen_ai.request.model"'), '') AS model,
	         session_id, start_ms
	  FROM spans ` + where + `
	),
	tok AS (
	  SELECT ss.model,
	    CAST(SUM(m.tokens_in) AS INTEGER)  AS tin,
	    CAST(SUM(m.tokens_out) AS INTEGER) AS tout,
	    ` + tokenSessionsAgg + ` AS tses
	  FROM (SELECT DISTINCT model, session_id FROM sk) ss
	  JOIN session_meta m ON m.session_id = ss.session_id
	  GROUP BY ss.model
	)
	SELECT sk.model,
	  COUNT(*),
	  COUNT(DISTINCT sk.session_id),
	  MAX(sk.start_ms),
	  -- tok emits exactly one row per model (it GROUPs BY ss.model), so MAX(t.*)
	  -- just lifts that single value through this GROUP BY — not a real aggregate.
	  -- If tok is ever changed to emit multiple rows per key, this must become SUM.
	  MAX(t.tin), MAX(t.tout), COALESCE(MAX(t.tses), 0)
	FROM sk LEFT JOIN tok t ON t.model = sk.model
	GROUP BY sk.model
	ORDER BY COUNT(*) DESC, sk.model ASC`

	rows, err := s.queryTokenRollup(ctx, q, args)
	if err != nil {
		return nil, fmt.Errorf("store: skill model rollup: %w", err)
	}
	defer rows.Close()

	var out []*SkillModelUsage
	for rows.Next() {
		m := &SkillModelUsage{}
		var tin, tout sql.NullInt64
		if err := rows.Scan(&m.Model, &m.Invocations, &m.Sessions, &m.LastFiredMs,
			&tin, &tout, &m.TokenSessions); err != nil {
			return nil, fmt.Errorf("store: scan model usage: %w", err)
		}
		m.InputTokens = nullInt64Ptr(tin)
		m.OutputTokens = nullInt64Ptr(tout)
		out = append(out, m)
	}
	return out, rows.Err()
}

// SkillVersionUsage is one skill's rollup per (ref, commit) version as
// recorded on its SKILL spans — the lineage view's data: how each pinned
// version behaved while it was the installed one. The empty (ref, commit)
// row groups invocations whose version is unknown (no load-path evidence).
type SkillVersionUsage struct {
	Ref          string // skill.version on the span (the lock Ref at fire time)
	Commit       string // skill.commit on the span
	Invocations  int64
	Sessions     int64
	FirstFiredMs int64
	LastFiredMs  int64
	// Session-attributed, keyed by this version's session set; nil = the
	// sessions reported no usage (n/a).
	InputTokens  *int64
	OutputTokens *int64
}

// SkillVersionRollup groups one skill's SKILL spans by the version identity
// they carried, newest-first by first-fired time. f.Skill is required.
func (s *sqliteStore) SkillVersionRollup(ctx context.Context, f *MetricsFilter) ([]*SkillVersionUsage, error) {
	if f == nil || f.Skill == "" {
		return nil, fmt.Errorf("store: skill version rollup: Skill is required")
	}
	where, args := skillSpanWhereSQL(f)
	q := `WITH ver AS (
	  SELECT ` + skillVersionExpr + ` AS ref,
	         COALESCE(json_extract(attributes, '$."skill.commit"'), '')  AS commit_sha,
	         session_id, start_ms
	  FROM spans ` + where + `
	),
	tok AS (
	  -- A session counts toward the unknown (ref = '') bucket only when none
	  -- of its spans carried a proven version; otherwise the same session's
	  -- tokens would be claimed by both the proven row and the unknown row.
	  SELECT vs.ref, vs.commit_sha,
	    CAST(SUM(m.tokens_in) AS INTEGER)  AS tin,
	    CAST(SUM(m.tokens_out) AS INTEGER) AS tout
	  FROM (SELECT DISTINCT ref, commit_sha, session_id FROM ver) vs
	  JOIN session_meta m ON m.session_id = vs.session_id
	  WHERE vs.ref <> ''
	     OR NOT EXISTS (SELECT 1 FROM ver pv
	                    WHERE pv.session_id = vs.session_id AND pv.ref <> '')
	  GROUP BY vs.ref, vs.commit_sha
	)
	SELECT ver.ref, ver.commit_sha,
	  COUNT(*),
	  COUNT(DISTINCT ver.session_id),
	  MIN(ver.start_ms),
	  MAX(ver.start_ms),
	  -- tok emits exactly one row per (ref, commit_sha) (it GROUPs BY vs.ref,
	  -- vs.commit_sha), so MAX(tok.*) just lifts that single value through this
	  -- GROUP BY — not a real aggregate. If tok is ever changed to emit multiple
	  -- rows per key, this must become SUM.
	  MAX(tok.tin),
	  MAX(tok.tout)
	FROM ver
	LEFT JOIN tok ON tok.ref = ver.ref AND tok.commit_sha = ver.commit_sha
	GROUP BY ver.ref, ver.commit_sha
	ORDER BY MIN(ver.start_ms) DESC`

	rows, err := s.queryTokenRollup(ctx, q, args)
	if err != nil {
		return nil, fmt.Errorf("store: skill version rollup: %w", err)
	}
	defer rows.Close()

	var out []*SkillVersionUsage
	for rows.Next() {
		v := &SkillVersionUsage{}
		var tin, tout sql.NullInt64
		if err := rows.Scan(&v.Ref, &v.Commit, &v.Invocations, &v.Sessions,
			&v.FirstFiredMs, &v.LastFiredMs, &tin, &tout); err != nil {
			return nil, fmt.Errorf("store: scan version usage: %w", err)
		}
		v.InputTokens = nullInt64Ptr(tin)
		v.OutputTokens = nullInt64Ptr(tout)
		out = append(out, v)
	}
	return out, rows.Err()
}
