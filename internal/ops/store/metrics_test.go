package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/astra-sh/qvr/internal/ops"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// The metrics fixture: three sessions across two working dirs.
//
//	sessA (projA, claude-code, day 2026-06-01):
//	  LLM turn (100in/40out) + SKILL pdf-tools @ c1/main, model fable-1
//	  LLM turn (50in/10out)  + SKILL pdf-tools, version unknown, model fable-2
//	sessB (projA, codex, day 2026-06-02, tokenless LLM spans):
//	  LLM turn (no usage attrs) + SKILL pdf-tools @ c2/main, model gpt-x
//	  SKILL changelog, version unknown, model gpt-x
//	sessC (projB, claude-code, day 2026-06-03):
//	  LLM turn (200in/80out) + SKILL changelog @ d1/main, model fable-1
//
// Expectations this encodes:
//   - pdf-tools: 3 invocations, 2 sessions, observed version "main" (deduped
//     across the two proven spans); changelog: 2 invocations, 2 sessions.
//   - tokens: pdf-tools = sessA's 150in/50out (sessB's spans carry NO usage
//     attrs, so it contributes NULL — absence, never a fabricated 0 — and is
//     excluded from the token-session count); changelog = sessC's 200in/80out.
//     A session firing two skills counts toward BOTH (session-level
//     attribution); a session firing one skill under two models likewise
//     counts toward both model rows (sessA: fable-1 AND fable-2).
//   - dirs scoping to projA hides sessC entirely.
var (
	mSessA = uuid.MustParse("00000000-0000-0000-0000-00000000000a")
	mSessB = uuid.MustParse("00000000-0000-0000-0000-00000000000b")
	mSessC = uuid.MustParse("00000000-0000-0000-0000-00000000000c")
)

const (
	projA = "/tmp/proj-a"
	projB = "/tmp/proj-b"
)

func msAt(day string, hour int) int64 {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		panic(err)
	}
	return t.Add(time.Duration(hour) * time.Hour).UnixMilli()
}

func seedMetricsFixture(t *testing.T, st Store) {
	t.Helper()
	ctx := context.Background()

	seedRaw := func(sid uuid.UUID, agent, dir string) {
		t.Helper()
		err := st.AppendRawTraces(ctx, []*ops.RawTrace{{
			ID:               uuid.New(),
			AgentName:        agent,
			SessionID:        sid,
			Source:           ops.RawSourceTranscript,
			SourcePath:       "/tmp/" + sid.String() + ".jsonl",
			WorkingDirectory: dir,
			Seq:              0,
			CapturedAt:       time.Now().UTC(),
			Raw:              []byte(`{}`),
		}}, nil)
		require.NoError(t, err)
	}
	seedRaw(mSessA, "claude-code", projA)
	seedRaw(mSessB, "codex", projA)
	seedRaw(mSessC, "claude-code", projB)

	llmAttrs := func(in, out int) string {
		return fmt.Sprintf(`{"gen_ai.operation.name":"chat","gen_ai.usage.input_tokens":%d,"gen_ai.usage.output_tokens":%d}`, in, out)
	}
	// skillAttrs mirrors v6 proof-gated spans: a span whose load path proved
	// the artifact carries the identity fields; one without evidence carries
	// the bare name only (version unknown). The turn's model rides on every
	// skill span (the model-cut key).
	skillAttrs := func(name string, proven bool, commit, ref, model string) string {
		if !proven {
			return fmt.Sprintf(`{"skill.name":%q,"gen_ai.request.model":%q}`, name, model)
		}
		return fmt.Sprintf(`{"skill.name":%q,"skill.commit":%q,"skill.version":%q,"gen_ai.request.model":%q}`,
			name, commit, ref, model)
	}
	span := func(sid uuid.UUID, agent, kind, id string, startMs int64, attrs string) *SpanRow {
		return &SpanRow{
			SpanID: id, TraceID: "tr-" + id, SessionID: sid, AgentName: agent,
			Kind: kind, Name: id, StartMs: startMs, EndMs: startMs, Attributes: attrs,
		}
	}
	// Session token totals as derive's fillMetaTokens would set them: the
	// session's LLM-span sums, nil when no span reported usage. The token
	// rollups read these (session_meta is the canonical per-session total).
	meta := func(sid uuid.UUID, agent, dir string, in, out *int64) *SessionMetaRow {
		return &SessionMetaRow{
			SessionID: sid, AgentName: agent, WorkingDir: dir,
			TokensIn: in, TokensOut: out, DeriverVersion: 7,
		}
	}
	require.NoError(t, st.ReplaceSessionDerivation(ctx, meta(mSessA, "claude-code", projA, i64(150), i64(50)), []*SpanRow{
		span(mSessA, "claude-code", "LLM", "a-llm-1", msAt("2026-06-01", 9), llmAttrs(100, 40)),
		span(mSessA, "claude-code", "SKILL", "a-skill-1", msAt("2026-06-01", 9), skillAttrs("pdf-tools", true, "c1", "main", "fable-1")),
		span(mSessA, "claude-code", "LLM", "a-llm-2", msAt("2026-06-01", 10), llmAttrs(50, 10)),
		// Same skill, no proof this time — bare name, no identity fields. A
		// different model than a-skill-1: the two-model-overlap case.
		span(mSessA, "claude-code", "SKILL", "a-skill-2", msAt("2026-06-01", 10), skillAttrs("pdf-tools", false, "", "", "fable-2")),
	}))
	require.NoError(t, st.ReplaceSessionDerivation(ctx, meta(mSessB, "codex", projA, nil, nil), []*SpanRow{
		// Codex-style tokenless LLM span: no gen_ai.usage.* attrs at all.
		span(mSessB, "codex", "LLM", "b-llm-1", msAt("2026-06-02", 12), `{"gen_ai.operation.name":"chat"}`),
		span(mSessB, "codex", "SKILL", "b-skill-1", msAt("2026-06-02", 12), skillAttrs("pdf-tools", true, "c2", "main", "gpt-x")),
		// Unverified — must not count toward Verified despite firing.
		span(mSessB, "codex", "SKILL", "b-skill-2", msAt("2026-06-02", 13), skillAttrs("changelog", false, "", "", "gpt-x")),
	}))
	require.NoError(t, st.ReplaceSessionDerivation(ctx, meta(mSessC, "claude-code", projB, i64(200), i64(80)), []*SpanRow{
		span(mSessC, "claude-code", "LLM", "c-llm-1", msAt("2026-06-03", 8), llmAttrs(200, 80)),
		span(mSessC, "claude-code", "SKILL", "c-skill-1", msAt("2026-06-03", 8), skillAttrs("changelog", true, "d1", "main", "fable-1")),
	}))
}

func TestSkillUsageRollup(t *testing.T) {
	st := openTestStore(t)
	seedMetricsFixture(t, st)
	ctx := context.Background()

	tests := []struct {
		name   string
		filter *MetricsFilter
		want   map[string]SkillUsage // keyed by skill; zero-value fields unchecked via map
	}{
		{
			name:   "all skills, no scope",
			filter: &MetricsFilter{},
			want: map[string]SkillUsage{
				"pdf-tools": {Invocations: 3, Sessions: 2, Versions: []string{"main"},
					FirstFiredMs: msAt("2026-06-01", 9), LastFiredMs: msAt("2026-06-02", 12)},
				"changelog": {Invocations: 2, Sessions: 2, Versions: []string{"main"},
					FirstFiredMs: msAt("2026-06-02", 13), LastFiredMs: msAt("2026-06-03", 8)},
			},
		},
		{
			name:   "dirs scoping excludes the other project",
			filter: &MetricsFilter{Dirs: []string{projA}},
			want: map[string]SkillUsage{
				"pdf-tools": {Invocations: 3, Sessions: 2, Versions: []string{"main"},
					FirstFiredMs: msAt("2026-06-01", 9), LastFiredMs: msAt("2026-06-02", 12)},
				// changelog's only projA invocation carries no identity — its
				// observed-version set is empty (the "unknown" rendering).
				"changelog": {Invocations: 1, Sessions: 1, Versions: nil,
					FirstFiredMs: msAt("2026-06-02", 13), LastFiredMs: msAt("2026-06-02", 13)},
			},
		},
		{
			name: "since/until windowing",
			filter: &MetricsFilter{
				Since: timePtr("2026-06-02T00:00:00Z"),
				Until: timePtr("2026-06-02T23:59:59Z"),
			},
			want: map[string]SkillUsage{
				"pdf-tools": {Invocations: 1, Sessions: 1, Versions: []string{"main"},
					FirstFiredMs: msAt("2026-06-02", 12), LastFiredMs: msAt("2026-06-02", 12)},
				"changelog": {Invocations: 1, Sessions: 1, Versions: nil,
					FirstFiredMs: msAt("2026-06-02", 13), LastFiredMs: msAt("2026-06-02", 13)},
			},
		},
		{
			name:   "single skill filter",
			filter: &MetricsFilter{Skill: "changelog"},
			want: map[string]SkillUsage{
				"changelog": {Invocations: 2, Sessions: 2, Versions: []string{"main"},
					FirstFiredMs: msAt("2026-06-02", 13), LastFiredMs: msAt("2026-06-03", 8)},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := st.SkillUsageRollup(ctx, tt.filter)
			require.NoError(t, err)
			require.Len(t, got, len(tt.want))
			for _, u := range got {
				w, ok := tt.want[u.Skill]
				require.True(t, ok, "unexpected skill %q", u.Skill)
				assert.Equal(t, w.Invocations, u.Invocations, "%s invocations", u.Skill)
				assert.Equal(t, w.Sessions, u.Sessions, "%s sessions", u.Skill)
				assert.Equal(t, w.Versions, u.Versions, "%s versions", u.Skill)
				assert.Equal(t, w.FirstFiredMs, u.FirstFiredMs, "%s firstFired", u.Skill)
				assert.Equal(t, w.LastFiredMs, u.LastFiredMs, "%s lastFired", u.Skill)
			}
		})
	}
}

func timePtr(rfc3339 string) *time.Time {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		panic(err)
	}
	return &t
}

// i64 builds the pointer form the NULL-preserving token fields use.
func i64(n int64) *int64 { return &n }

func TestSkillTokenRollup(t *testing.T) {
	st := openTestStore(t)
	seedMetricsFixture(t, st)
	ctx := context.Background()

	got, err := st.SkillTokenRollup(ctx, &MetricsFilter{})
	require.NoError(t, err)

	// pdf-tools fired in sessA (150in/50out across two LLM spans — summed once,
	// never doubled by the two SKILL spans in the same session) and sessB,
	// whose codex LLM span carries NO usage attrs: it adds nothing to the sums
	// and does not count as a token session.
	pdf := got["pdf-tools"]
	require.NotNil(t, pdf)
	assert.Equal(t, i64(150), pdf.InputTokens)
	assert.Equal(t, i64(50), pdf.OutputTokens)
	assert.Equal(t, int64(1), pdf.Sessions, "only the usage-reporting session counts")

	// changelog fired in sessB (no usage) and sessC: the sessB overlap with
	// pdf-tools counts toward BOTH skills — intentional session-level
	// attribution — but only sessC reports usage.
	cl := got["changelog"]
	require.NotNil(t, cl)
	assert.Equal(t, i64(200), cl.InputTokens)
	assert.Equal(t, i64(80), cl.OutputTokens)
	assert.Equal(t, int64(1), cl.Sessions)
}

func TestSkillTokenRollup_DirsScoped(t *testing.T) {
	st := openTestStore(t)
	seedMetricsFixture(t, st)

	got, err := st.SkillTokenRollup(context.Background(), &MetricsFilter{Dirs: []string{projB}})
	require.NoError(t, err)
	require.Nil(t, got["pdf-tools"], "pdf-tools never fired in projB")
	cl := got["changelog"]
	require.NotNil(t, cl)
	assert.Equal(t, i64(200), cl.InputTokens)
	assert.Equal(t, i64(80), cl.OutputTokens)
	assert.Equal(t, int64(1), cl.Sessions)
}

// TestSkillTokenRollup_AbsenceVsZero pins the n/a invariant at the SQL layer:
// a skill whose only sessions carry no usage attrs rolls up to nil sides —
// while a session that genuinely reported usage:{0,0} rolls up to a real 0.
func TestSkillTokenRollup_AbsenceVsZero(t *testing.T) {
	st := openTestStore(t)
	seedMetricsFixture(t, st)
	ctx := context.Background()

	got, err := st.SkillTokenRollup(ctx, &MetricsFilter{Dirs: []string{projA}, Skill: "changelog"})
	require.NoError(t, err)
	// In projA changelog fired only in the token-less codex session.
	cl := got["changelog"]
	require.NotNil(t, cl)
	assert.Nil(t, cl.InputTokens, "no usage reported → nil, never 0")
	assert.Nil(t, cl.OutputTokens)
	assert.Equal(t, int64(0), cl.Sessions)

	// A genuine zero-usage session: usage attrs present, both sides 0.
	sessZ := uuid.MustParse("00000000-0000-0000-0000-00000000000d")
	require.NoError(t, st.AppendRawTraces(ctx, []*ops.RawTrace{{
		ID: uuid.New(), AgentName: "claude-code", SessionID: sessZ,
		Source: ops.RawSourceTranscript, SourcePath: "/tmp/z.jsonl",
		WorkingDirectory: projA, CapturedAt: time.Now().UTC(), Raw: []byte(`{}`),
	}}, nil))
	require.NoError(t, st.ReplaceSessionDerivation(ctx, &SessionMetaRow{
		SessionID: sessZ, AgentName: "claude-code", WorkingDir: projA,
		TokensIn: i64(0), TokensOut: i64(0), DeriverVersion: 7,
	}, []*SpanRow{
		{SpanID: "z-llm-1", TraceID: "tr-z", SessionID: sessZ, AgentName: "claude-code",
			Kind: "LLM", Name: "z-llm-1", StartMs: msAt("2026-06-04", 9), EndMs: msAt("2026-06-04", 9),
			Attributes: `{"gen_ai.operation.name":"chat","gen_ai.usage.input_tokens":0,"gen_ai.usage.output_tokens":0}`},
		{SpanID: "z-skill-1", TraceID: "tr-z", SessionID: sessZ, AgentName: "claude-code",
			Kind: "SKILL", Name: "z-skill-1", StartMs: msAt("2026-06-04", 9), EndMs: msAt("2026-06-04", 9),
			Attributes: `{"skill.name":"zero-skill"}`},
	}))
	got, err = st.SkillTokenRollup(ctx, &MetricsFilter{Skill: "zero-skill"})
	require.NoError(t, err)
	z := got["zero-skill"]
	require.NotNil(t, z)
	assert.Equal(t, i64(0), z.InputTokens, "a reported 0 stays 0, distinct from absent")
	assert.Equal(t, i64(0), z.OutputTokens)
	assert.Equal(t, int64(1), z.Sessions)
}

func TestSkillInvocationSeries(t *testing.T) {
	st := openTestStore(t)
	seedMetricsFixture(t, st)
	ctx := context.Background()

	_, err := st.SkillInvocationSeries(ctx, &MetricsFilter{})
	require.Error(t, err, "Skill is required")

	got, err := st.SkillInvocationSeries(ctx, &MetricsFilter{Skill: "pdf-tools"})
	require.NoError(t, err)
	require.Len(t, got, 2)
	// Oldest day first; one bucket per (day, agent).
	assert.Equal(t, "2026-06-01", got[0].Day)
	assert.Equal(t, "claude-code", got[0].Agent)
	assert.Equal(t, int64(2), got[0].Invocations)
	assert.Equal(t, "2026-06-02", got[1].Day)
	assert.Equal(t, "codex", got[1].Agent)
	assert.Equal(t, int64(1), got[1].Invocations)
}

func TestSkillAgentRollup(t *testing.T) {
	st := openTestStore(t)
	seedMetricsFixture(t, st)
	ctx := context.Background()

	_, err := st.SkillAgentRollup(ctx, &MetricsFilter{})
	require.Error(t, err, "Skill is required")

	got, err := st.SkillAgentRollup(ctx, &MetricsFilter{Skill: "pdf-tools"})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "claude-code", got[0].Agent, "busiest agent first")
	assert.Equal(t, int64(2), got[0].Invocations)
	// One proven span (@ main) + one unknown → the agent's observed set is
	// the proven refs only; unknown never fabricates an entry.
	assert.Equal(t, []string{"main"}, got[0].Versions)
	assert.Equal(t, int64(1), got[0].Sessions)
	// claude's session reported usage; its tokens land on the agent cut.
	assert.Equal(t, i64(150), got[0].InputTokens)
	assert.Equal(t, i64(50), got[0].OutputTokens)
	assert.Equal(t, int64(1), got[0].TokenSessions)
	assert.Equal(t, "codex", got[1].Agent)
	assert.Equal(t, int64(1), got[1].Invocations)
	assert.Equal(t, []string{"main"}, got[1].Versions)
	// codex's store reported no usage — n/a, never 0.
	assert.Nil(t, got[1].InputTokens)
	assert.Nil(t, got[1].OutputTokens)
	assert.Equal(t, int64(0), got[1].TokenSessions)
}

// TestSkillModelRollup pins the model cut: per-model invocations with
// session-attributed tokens, where one session firing the skill under two
// models contributes its tokens to BOTH rows (exposure, not exclusive cost).
func TestSkillModelRollup(t *testing.T) {
	st := openTestStore(t)
	seedMetricsFixture(t, st)
	ctx := context.Background()

	_, err := st.SkillModelRollup(ctx, &MetricsFilter{})
	require.Error(t, err, "Skill is required")

	got, err := st.SkillModelRollup(ctx, &MetricsFilter{Skill: "pdf-tools"})
	require.NoError(t, err)
	require.Len(t, got, 3)
	byModel := map[string]*SkillModelUsage{}
	for _, m := range got {
		byModel[m.Model] = m
	}

	// sessA fired pdf-tools under fable-1 AND fable-2: the whole session's
	// tokens appear on both rows.
	for _, model := range []string{"fable-1", "fable-2"} {
		m := byModel[model]
		require.NotNil(t, m, model)
		assert.Equal(t, int64(1), m.Invocations, model)
		assert.Equal(t, int64(1), m.Sessions, model)
		assert.Equal(t, i64(150), m.InputTokens, model)
		assert.Equal(t, i64(50), m.OutputTokens, model)
		assert.Equal(t, int64(1), m.TokenSessions, model)
	}
	// The codex session reports no usage → its model row reads n/a.
	gx := byModel["gpt-x"]
	require.NotNil(t, gx)
	assert.Equal(t, int64(1), gx.Invocations)
	assert.Nil(t, gx.InputTokens)
	assert.Nil(t, gx.OutputTokens)
	assert.Equal(t, int64(0), gx.TokenSessions)
}

// TestSkillAgentRollup_NoIdentity pins the unknown rendering's data shape: an
// agent whose invocations never carried identity has an EMPTY Versions set.
func TestSkillAgentRollup_NoIdentity(t *testing.T) {
	st := openTestStore(t)
	seedMetricsFixture(t, st)

	got, err := st.SkillAgentRollup(context.Background(),
		&MetricsFilter{Skill: "changelog", Dirs: []string{projA}})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "codex", got[0].Agent)
	assert.Empty(t, got[0].Versions, "no proven identity → no observed versions")
}

func TestSkillVersionRollup(t *testing.T) {
	st := openTestStore(t)
	seedMetricsFixture(t, st)
	ctx := context.Background()

	_, err := st.SkillVersionRollup(ctx, &MetricsFilter{})
	require.Error(t, err, "Skill is required")

	got, err := st.SkillVersionRollup(ctx, &MetricsFilter{Skill: "pdf-tools"})
	require.NoError(t, err)
	require.Len(t, got, 3, "two proven commits + the unverified (no-identity) bucket")

	// Newest-first by first-fired: c2 (06-02 12h), the unknown-version bucket
	// (06-01 10h), then c1 (06-01 9h).
	c2, unk, c1 := got[0], got[1], got[2]
	assert.Equal(t, "c2", c2.Commit)
	assert.Equal(t, int64(1), c2.Invocations)
	// c2 fired only in the codex session, whose spans carry no usage attrs —
	// nil (n/a), never a fabricated 0.
	assert.Nil(t, c2.InputTokens)
	assert.Nil(t, c2.OutputTokens)

	// The identity-less invocation groups under empty ref+commit — the
	// lineage view's honest "version unknown" row.
	assert.Equal(t, "", unk.Commit)
	assert.Equal(t, "", unk.Ref)
	assert.Equal(t, int64(1), unk.Invocations)
	// sessA also fired a proven version (c1), so the unknown bucket must not
	// re-claim that session's tokens — they stay with the proven row only.
	assert.Nil(t, unk.InputTokens)
	assert.Nil(t, unk.OutputTokens)

	assert.Equal(t, "c1", c1.Commit)
	assert.Equal(t, "main", c1.Ref)
	assert.Equal(t, int64(1), c1.Invocations)
	assert.Equal(t, int64(1), c1.Sessions)
	// c1's session set is sessA → its full LLM token sums, counted once.
	assert.Equal(t, i64(150), c1.InputTokens)
	assert.Equal(t, i64(50), c1.OutputTokens)
}

// TestSkillDurationRollups checks both duration measures: the exclusive
// skill-span self-latency (a point span is excluded from the stats but still
// counts as an invocation) and the session-attributed wall-clock.
func TestSkillDurationRollups(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	const skillAttr = `{"skill.name":"probe"}`

	// Session 1: 60s wall-clock; one 5s SKILL span + one point span (excluded).
	s1 := uuid.New()
	st1 := msAt("2026-06-01", 9)
	require.NoError(t, st.ReplaceSessionDerivation(ctx, &SessionMetaRow{
		SessionID: s1, AgentName: "claude-code", StartedMs: st1, EndedMs: st1 + 60_000, DeriverVersion: 7,
	}, []*SpanRow{
		{SpanID: "p1-a", TraceID: "t1", SessionID: s1, AgentName: "claude-code", Kind: "SKILL",
			Name: "p1-a", StartMs: st1, EndMs: st1 + 5_000, Attributes: skillAttr},
		{SpanID: "p1-b", TraceID: "t1", SessionID: s1, AgentName: "claude-code", Kind: "SKILL",
			Name: "p1-b", StartMs: st1 + 6_000, EndMs: st1 + 6_000, Attributes: skillAttr}, // point → excluded
	}))

	// Session 2: 120s wall-clock; one 3s SKILL span.
	s2 := uuid.New()
	st2 := msAt("2026-06-02", 9)
	require.NoError(t, st.ReplaceSessionDerivation(ctx, &SessionMetaRow{
		SessionID: s2, AgentName: "codex", StartedMs: st2, EndedMs: st2 + 120_000, DeriverVersion: 7,
	}, []*SpanRow{
		{SpanID: "p2-a", TraceID: "t2", SessionID: s2, AgentName: "codex", Kind: "SKILL",
			Name: "p2-a", StartMs: st2, EndMs: st2 + 3_000, Attributes: skillAttr},
	}))

	usage, err := st.SkillUsageRollup(ctx, &MetricsFilter{Skill: "probe"})
	require.NoError(t, err)
	require.Len(t, usage, 1)
	require.Equal(t, int64(3), usage[0].Invocations, "all 3 SKILL spans count as invocations")
	lat := usage[0].Latency
	require.Equal(t, int64(2), lat.Measured, "point span excluded from measured latency")
	require.Equal(t, int64(3_000), lat.MinMs)
	require.Equal(t, int64(5_000), lat.MaxMs)
	require.Equal(t, int64(8_000), lat.TotalMs)
	require.Equal(t, int64(4_000), lat.AvgMs)

	durs, err := st.SkillSessionDurationRollup(ctx, &MetricsFilter{Skill: "probe"})
	require.NoError(t, err)
	d := durs["probe"]
	require.NotNil(t, d)
	require.Equal(t, int64(2), d.Measured)
	require.Equal(t, int64(60_000), d.MinMs)
	require.Equal(t, int64(120_000), d.MaxMs)
	require.Equal(t, int64(180_000), d.TotalMs)
	require.Equal(t, int64(90_000), d.AvgMs)
}
