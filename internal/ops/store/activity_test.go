package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// seedActivity writes a session_meta row at startedMs lasting durMs.
func seedActivity(t *testing.T, st Store, agent string, startedMs, durMs int64, turns int64, skills ...string) {
	t.Helper()
	m := &SessionMetaRow{
		SessionID:  uuid.New(),
		AgentName:  agent,
		WorkingDir: "/tmp/proj",
		StartedMs:  startedMs,
		EndedMs:    startedMs + durMs,
		Turns:      turns,
		Tools:      turns * 2,
		Skills:     skills,
	}
	if err := st.ReplaceSessionDerivation(context.Background(), m, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// seedThreeSessions plants the shared fixture: two Monday claude sessions
// (one skill-less) plus one Tuesday codex session.
func seedThreeSessions(t *testing.T, st Store) {
	t.Helper()
	mon930 := time.Date(2026, 6, 1, 9, 30, 0, 0, time.UTC).UnixMilli()
	mon13 := time.Date(2026, 6, 1, 13, 0, 0, 0, time.UTC).UnixMilli()
	tue10 := time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC).UnixMilli()
	seedActivity(t, st, "claude", mon930, 60_000, 2, "code-review")
	seedActivity(t, st, "claude", mon13, 120_000, 3) // skill-less (kept via keep-all)
	seedActivity(t, st, "codex", tue10, 30_000, 1, "tdd")
}

func TestActivity_Summary(t *testing.T) {
	st := openTestStore(t)
	seedThreeSessions(t, st)

	sum, err := st.ActivitySummary(context.Background(), nil)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.Sessions != 3 || sum.SkillSessions != 2 || sum.Turns != 6 || sum.Tools != 12 {
		t.Errorf("summary = %+v, want 3 sessions / 2 skill / 6 turns / 12 tools", sum)
	}
	if sum.DurationMs != 210_000 {
		t.Errorf("duration = %d, want 210000", sum.DurationMs)
	}
	if len(sum.Agents) != 2 || sum.Agents[0].Agent != "claude" || sum.Agents[0].Sessions != 2 {
		t.Errorf("agents = %+v, want claude first with 2 sessions", sum.Agents)
	}
	if sum.Agents[0].AvgSessionMs != 90_000 {
		t.Errorf("claude avg = %d, want 90000", sum.Agents[0].AvgSessionMs)
	}
	if sum.Agents[0].SkillSessions != 1 || sum.Agents[1].SkillSessions != 1 {
		t.Errorf("per-agent skill sessions = %d/%d, want 1/1",
			sum.Agents[0].SkillSessions, sum.Agents[1].SkillSessions)
	}
}

func TestActivity_Series(t *testing.T) {
	st := openTestStore(t)
	seedThreeSessions(t, st)

	series, err := st.ActivitySeries(context.Background(), nil)
	if err != nil {
		t.Fatalf("series: %v", err)
	}
	if len(series) != 2 {
		t.Fatalf("series = %+v, want 2 (day,agent) buckets", series)
	}
	if series[0].Day != "2026-06-01" || series[0].Agent != "claude" ||
		series[0].Sessions != 2 || series[0].SkillSessions != 1 || series[0].Turns != 5 {
		t.Errorf("monday bucket = %+v", series[0])
	}
	if series[1].Day != "2026-06-02" || series[1].Agent != "codex" || series[1].Sessions != 1 {
		t.Errorf("tuesday bucket = %+v", series[1])
	}
}

func TestActivity_FiltersAndWindow(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	day1 := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC).UnixMilli()
	day2 := time.Date(2026, 6, 2, 9, 0, 0, 0, time.UTC).UnixMilli()
	seedActivity(t, st, "claude", day1, 1000, 1, "a")
	seedActivity(t, st, "codex", day2, 1000, 1, "b")

	since := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	sum, err := st.ActivitySummary(ctx, &ActivityFilter{Since: &since})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.Sessions != 1 || sum.Agents[0].Agent != "codex" {
		t.Errorf("since filter = %+v, want only codex", sum)
	}

	sum, err = st.ActivitySummary(ctx, &ActivityFilter{Agents: []string{"claude"}})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.Sessions != 1 || sum.Agents[0].Agent != "claude" {
		t.Errorf("agent filter = %+v, want only claude", sum)
	}

	sum, err = st.ActivitySummary(ctx, &ActivityFilter{Dirs: []string{"/elsewhere"}})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.Sessions != 0 {
		t.Errorf("dir filter = %+v, want none", sum)
	}
}

func TestActivity_SkippedSeries(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	day := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	rows := []*ScannedFile{
		{AgentName: "claude", SourcePath: "/a.jsonl", Size: 1, MtimeMs: day, Status: ScanStatusSkipped},
		{AgentName: "claude", SourcePath: "/b.jsonl", Size: 1, MtimeMs: day, Status: ScanStatusSkipped},
		{AgentName: "claude", SourcePath: "/c.jsonl", Size: 1, MtimeMs: day, Status: ScanStatusIngested}, // not skipped
	}
	for _, r := range rows {
		if err := st.UpsertScannedFile(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.SkippedSkilllessSeries(ctx, nil, nil)
	if err != nil {
		t.Fatalf("skipped series: %v", err)
	}
	if len(got) != 1 || got[0].Day != "2026-06-01" || got[0].Agent != "claude" || got[0].Sessions != 2 {
		t.Errorf("skipped series = %+v, want one bucket of 2", got)
	}
}

// TestActivity_Tokens pins the token totals: summed from the scoped sessions'
// LLM spans (real usage), with project scoping applied through session_meta.
func TestActivity_Tokens(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	sid := uuid.New()
	meta := &SessionMetaRow{
		SessionID: sid, AgentName: "claude", WorkingDir: "/tmp/proj",
		StartedMs: 1000, EndedMs: 2000, Turns: 1, Skills: []string{"a"},
	}
	spans := []*SpanRow{
		{SpanID: "l1", TraceID: "t", SessionID: sid, AgentName: "claude", Kind: "LLM",
			StartMs: 1000, EndMs: 2000,
			Attributes: `{"gen_ai.usage.input_tokens":120,"gen_ai.usage.output_tokens":30}`},
		{SpanID: "l2", TraceID: "t", SessionID: sid, AgentName: "claude", Kind: "LLM",
			StartMs: 1500, EndMs: 1800,
			Attributes: `{"gen_ai.usage.input_tokens":80,"gen_ai.usage.output_tokens":20}`},
		{SpanID: "t1", TraceID: "t", SessionID: sid, AgentName: "claude", Kind: "TOOL",
			StartMs: 1500, EndMs: 1600,
			Attributes: `{"gen_ai.usage.input_tokens":999}`}, // non-LLM: ignored
	}
	if err := st.ReplaceSessionDerivation(ctx, meta, spans); err != nil {
		t.Fatal(err)
	}

	sum, err := st.ActivitySummary(ctx, nil)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.TokensIn != 200 || sum.TokensOut != 50 {
		t.Errorf("tokens = %d in / %d out, want 200/50", sum.TokensIn, sum.TokensOut)
	}

	scoped, err := st.ActivitySummary(ctx, &ActivityFilter{Dirs: []string{"/elsewhere"}})
	if err != nil {
		t.Fatalf("scoped summary: %v", err)
	}
	if scoped.TokensIn != 0 || scoped.TokensOut != 0 {
		t.Errorf("scoped tokens = %d/%d, want 0/0", scoped.TokensIn, scoped.TokensOut)
	}
}
