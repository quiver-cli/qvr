package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func metaFor(sid uuid.UUID, agent string, startedMs int64, skills ...string) *SessionMetaRow {
	return &SessionMetaRow{
		SessionID:       sid,
		AgentName:       agent,
		SourceSessionID: "src-" + sid.String()[:8],
		SourcePath:      "/store/" + sid.String()[:8] + ".jsonl",
		WorkingDir:      "/tmp/proj",
		Model:           "model-x",
		Title:           "do the thing",
		StartedMs:       startedMs,
		EndedMs:         startedMs + 1000,
		Turns:           2,
		Tools:           3,
		Skills:          skills,
		DeriverVersion:  1,
	}
}

// TestReplaceSessionDerivation_RoundTrip pins the unified write path: meta +
// spans land atomically and read back exactly, and a re-derivation replaces
// (not accretes) both.
func TestReplaceSessionDerivation_RoundTrip(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	sid := uuid.New()

	meta := metaFor(sid, "claude-code", 1000, "code-review")
	spans := []*SpanRow{
		{SpanID: "s1", TraceID: "t", SessionID: sid, AgentName: "claude-code", Kind: "LLM", Name: "turn", StartMs: 1000, EndMs: 2000},
		{SpanID: "s2", TraceID: "t", SessionID: sid, AgentName: "claude-code", Kind: "SKILL", Name: "code-review", StartMs: 1100, EndMs: 1200},
	}
	if err := st.ReplaceSessionDerivation(ctx, meta, spans); err != nil {
		t.Fatalf("replace derivation: %v", err)
	}

	assertMetaRoundTrip(t, st, sid)

	gotSpans, err := st.QuerySpans(ctx, &SpanFilter{SessionID: &sid})
	if err != nil {
		t.Fatalf("query spans: %v", err)
	}
	if len(gotSpans) != 2 {
		t.Fatalf("want 2 spans, got %d", len(gotSpans))
	}

	// Re-derive with fewer spans and no skills: both projections must shrink.
	meta2 := metaFor(sid, "claude-code", 1000)
	meta2.Turns = 1
	if err := st.ReplaceSessionDerivation(ctx, meta2, spans[:1]); err != nil {
		t.Fatalf("re-derive: %v", err)
	}
	got2, _ := st.GetSessionMeta(ctx, sid)
	if got2 == nil || got2.Turns != 1 || len(got2.Skills) != 0 {
		t.Errorf("re-derivation did not replace meta: %+v", got2)
	}
	gotSpans2, _ := st.QuerySpans(ctx, &SpanFilter{SessionID: &sid})
	if len(gotSpans2) != 1 {
		t.Errorf("re-derivation did not replace spans: %d", len(gotSpans2))
	}
}

// assertMetaRoundTrip checks the written meta reads back field-for-field.
func assertMetaRoundTrip(t *testing.T, st Store, sid uuid.UUID) {
	t.Helper()
	got, err := st.GetSessionMeta(context.Background(), sid)
	if err != nil {
		t.Fatalf("get meta: %v", err)
	}
	if got == nil {
		t.Fatal("meta not found after write")
	}
	if got.AgentName != "claude-code" || got.Title != "do the thing" ||
		got.Model != "model-x" || got.StartedMs != 1000 || got.EndedMs != 2000 ||
		got.Turns != 2 || got.Tools != 3 {
		t.Errorf("meta did not round-trip: %+v", got)
	}
	if len(got.Skills) != 1 || got.Skills[0] != "code-review" {
		t.Errorf("skills did not round-trip: %v", got.Skills)
	}
	if got.SourceSessionID == "" || got.SourcePath == "" || got.WorkingDir != "/tmp/proj" {
		t.Errorf("source identity did not round-trip: %+v", got)
	}
}

// TestListSessionMeta_Filters exercises agent/dir/skill/window scoping over
// the unified read model.
func TestListSessionMeta_Filters(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	a, b, c := uuid.New(), uuid.New(), uuid.New()
	mustDerive := func(m *SessionMetaRow) {
		t.Helper()
		if err := st.ReplaceSessionDerivation(ctx, m, nil); err != nil {
			t.Fatalf("derive %s: %v", m.SessionID, err)
		}
	}
	mustDerive(metaFor(a, "claude-code", 1000, "code-review"))
	mustDerive(metaFor(b, "codex", 2000, "tdd", "code-review"))
	skillless := metaFor(c, "claude-code", 3000)
	skillless.WorkingDir = "/tmp/other"
	mustDerive(skillless)

	cases := []struct {
		name   string
		filter *SessionMetaFilter
		want   []uuid.UUID // newest-first
	}{
		{"all newest-first", nil, []uuid.UUID{c, b, a}},
		{"agent", &SessionMetaFilter{Agent: "codex"}, []uuid.UUID{b}},
		{"dir", &SessionMetaFilter{Dirs: []string{"/tmp/other"}}, []uuid.UUID{c}},
		{"skill", &SessionMetaFilter{Skill: "code-review"}, []uuid.UUID{b, a}},
		{"skills-only", &SessionMetaFilter{SkillsOnly: true}, []uuid.UUID{b, a}},
		{"since", &SessionMetaFilter{Since: msPtr(1500)}, []uuid.UUID{c, b}},
		{"until", &SessionMetaFilter{Until: msPtr(1500)}, []uuid.UUID{a}},
		{"limit", &SessionMetaFilter{Limit: 1}, []uuid.UUID{c}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := st.ListSessionMeta(ctx, tc.filter)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("want %d rows, got %d", len(tc.want), len(got))
			}
			for i, w := range tc.want {
				if got[i].SessionID != w {
					t.Errorf("row %d: want %s, got %s", i, w, got[i].SessionID)
				}
			}
		})
	}
}

// TestGetSessionMeta_AbsentIsNil pins the no-row contract: nil, not an error.
func TestGetSessionMeta_AbsentIsNil(t *testing.T) {
	st := openTestStore(t)
	got, err := st.GetSessionMeta(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("get absent meta: %v", err)
	}
	if got != nil {
		t.Errorf("want nil for absent session, got %+v", got)
	}
}

// TestDeleteSession_RemovesMeta proves DeleteSession clears the unified row
// alongside raw/spans/cursor.
func TestDeleteSession_RemovesMeta(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	sid := uuid.New()
	if err := st.ReplaceSessionDerivation(ctx, metaFor(sid, "codex", 1000, "tdd"), nil); err != nil {
		t.Fatalf("derive: %v", err)
	}
	if _, err := st.DeleteSession(ctx, sid); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	got, err := st.GetSessionMeta(ctx, sid)
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if got != nil {
		t.Errorf("session_meta row survived DeleteSession: %+v", got)
	}
}

func msPtr(ms int64) *time.Time {
	t := time.UnixMilli(ms).UTC()
	return &t
}
