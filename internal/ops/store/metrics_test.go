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
//	  LLM turn (100in/40out) + SKILL pdf-tools, proven identity @ commit c1/main
//	  LLM turn (50in/10out)  + SKILL pdf-tools, version unknown (bare name)
//	sessB (projA, codex, day 2026-06-02, tokenless LLM spans):
//	  LLM turn (no usage attrs) + SKILL pdf-tools, proven identity @ commit c2/main
//	  SKILL changelog, version unknown (bare name)
//	sessC (projB, claude-code, day 2026-06-03):
//	  LLM turn (200in/80out) + SKILL changelog, proven identity @ commit d1/main
//
// Expectations this encodes:
//   - pdf-tools: 3 invocations, 2 sessions, observed version "main" (deduped
//     across the two proven spans); changelog: 2 invocations, 2 sessions.
//   - tokens: pdf-tools = sessA+sessB LLM sums = 150in/50out (sessB contributes 0);
//     changelog = sessB+sessC = 200in/80out. sessB overlap counts toward BOTH
//     skills (intentional session-level attribution).
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
	// the bare name only (version unknown).
	skillAttrs := func(name string, proven bool, commit, ref string) string {
		if !proven {
			return fmt.Sprintf(`{"skill.name":%q}`, name)
		}
		return fmt.Sprintf(`{"skill.name":%q,"skill.commit":%q,"skill.version":%q}`, name, commit, ref)
	}
	span := func(sid uuid.UUID, agent, kind, id string, startMs int64, attrs string) *SpanRow {
		return &SpanRow{
			SpanID: id, TraceID: "tr-" + id, SessionID: sid, AgentName: agent,
			Kind: kind, Name: id, StartMs: startMs, EndMs: startMs, Attributes: attrs,
		}
	}

	require.NoError(t, st.ReplaceSessionSpans(ctx, mSessA, []*SpanRow{
		span(mSessA, "claude-code", "LLM", "a-llm-1", msAt("2026-06-01", 9), llmAttrs(100, 40)),
		span(mSessA, "claude-code", "SKILL", "a-skill-1", msAt("2026-06-01", 9), skillAttrs("pdf-tools", true, "c1", "main")),
		span(mSessA, "claude-code", "LLM", "a-llm-2", msAt("2026-06-01", 10), llmAttrs(50, 10)),
		// Same skill, no proof this time — bare name, no identity fields.
		span(mSessA, "claude-code", "SKILL", "a-skill-2", msAt("2026-06-01", 10), skillAttrs("pdf-tools", false, "", "")),
	}))
	require.NoError(t, st.ReplaceSessionSpans(ctx, mSessB, []*SpanRow{
		// Codex-style tokenless LLM span: no gen_ai.usage.* attrs at all.
		span(mSessB, "codex", "LLM", "b-llm-1", msAt("2026-06-02", 12), `{"gen_ai.operation.name":"chat"}`),
		span(mSessB, "codex", "SKILL", "b-skill-1", msAt("2026-06-02", 12), skillAttrs("pdf-tools", true, "c2", "main")),
		// Unverified — must not count toward Verified despite firing.
		span(mSessB, "codex", "SKILL", "b-skill-2", msAt("2026-06-02", 13), skillAttrs("changelog", false, "", "")),
	}))
	require.NoError(t, st.ReplaceSessionSpans(ctx, mSessC, []*SpanRow{
		span(mSessC, "claude-code", "LLM", "c-llm-1", msAt("2026-06-03", 8), llmAttrs(200, 80)),
		span(mSessC, "claude-code", "SKILL", "c-skill-1", msAt("2026-06-03", 8), skillAttrs("changelog", true, "d1", "main")),
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

func TestSkillTokenRollup(t *testing.T) {
	st := openTestStore(t)
	seedMetricsFixture(t, st)
	ctx := context.Background()

	got, err := st.SkillTokenRollup(ctx, &MetricsFilter{})
	require.NoError(t, err)

	// pdf-tools fired in sessA (150in/50out across two LLM spans — summed once,
	// never doubled by the two SKILL spans in the same session) and sessB
	// (tokenless codex LLM span → contributes 0 but still counts the session).
	pdf := got["pdf-tools"]
	require.NotNil(t, pdf)
	assert.Equal(t, int64(150), pdf.InputTokens)
	assert.Equal(t, int64(50), pdf.OutputTokens)
	assert.Equal(t, int64(2), pdf.Sessions)

	// changelog fired in sessB and sessC: the sessB overlap with pdf-tools
	// counts toward BOTH skills — intentional session-level attribution.
	cl := got["changelog"]
	require.NotNil(t, cl)
	assert.Equal(t, int64(200), cl.InputTokens)
	assert.Equal(t, int64(80), cl.OutputTokens)
	assert.Equal(t, int64(2), cl.Sessions)
}

func TestSkillTokenRollup_DirsScoped(t *testing.T) {
	st := openTestStore(t)
	seedMetricsFixture(t, st)

	got, err := st.SkillTokenRollup(context.Background(), &MetricsFilter{Dirs: []string{projB}})
	require.NoError(t, err)
	require.Nil(t, got["pdf-tools"], "pdf-tools never fired in projB")
	cl := got["changelog"]
	require.NotNil(t, cl)
	assert.Equal(t, int64(200), cl.InputTokens)
	assert.Equal(t, int64(80), cl.OutputTokens)
	assert.Equal(t, int64(1), cl.Sessions)
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
	assert.Equal(t, "codex", got[1].Agent)
	assert.Equal(t, int64(1), got[1].Invocations)
	assert.Equal(t, []string{"main"}, got[1].Versions)
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
	// c2 fired only in the tokenless codex session → zero tokens.
	assert.Equal(t, int64(0), c2.InputTokens)
	assert.Equal(t, int64(0), c2.OutputTokens)

	// The identity-less invocation groups under empty ref+commit — the
	// lineage view's honest "version unknown" row.
	assert.Equal(t, "", unk.Commit)
	assert.Equal(t, "", unk.Ref)
	assert.Equal(t, int64(1), unk.Invocations)
	// sessA also fired a proven version (c1), so the unknown bucket must not
	// re-claim that session's tokens — they stay with the proven row only.
	assert.Equal(t, int64(0), unk.InputTokens)
	assert.Equal(t, int64(0), unk.OutputTokens)

	assert.Equal(t, "c1", c1.Commit)
	assert.Equal(t, "main", c1.Ref)
	assert.Equal(t, int64(1), c1.Invocations)
	assert.Equal(t, int64(1), c1.Sessions)
	// c1's session set is sessA → its full LLM token sums, counted once.
	assert.Equal(t, int64(150), c1.InputTokens)
	assert.Equal(t, int64(50), c1.OutputTokens)
}
