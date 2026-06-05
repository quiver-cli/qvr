package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/raks097/quiver/internal/ops"
)

// seedSession writes a transcript row (so the session exists) and one span,
// optionally skill-attributed, for the filter/skills tests.
func seedSession(t *testing.T, st Store, id uuid.UUID, agent, dir string, at time.Time, skill string) {
	t.Helper()
	ctx := context.Background()
	row := &ops.RawTrace{
		AgentName:        agent,
		SessionID:        id,
		Source:           ops.RawSourceTranscript,
		WorkingDirectory: dir,
		CapturedAt:       at,
		Raw:              []byte(`{"type":"user","message":{"role":"user","content":"hi"}}`),
	}
	if err := st.AppendRawTraces(ctx, []*ops.RawTrace{row}, nil); err != nil {
		t.Fatalf("seed raw: %v", err)
	}
	attrs := "{}"
	if skill != "" {
		attrs = `{"skill.name":"` + skill + `"}`
	}
	span := &SpanRow{
		SpanID: "sp-" + id.String(), TraceID: "tr", SessionID: id,
		AgentName: agent, Kind: "SKILL", Name: "execute_tool Skill",
		StartMs: 1, EndMs: 2, Attributes: attrs, DeriverVersion: 4,
	}
	if err := st.ReplaceSessionSpans(ctx, id, []*SpanRow{span}); err != nil {
		t.Fatalf("seed span: %v", err)
	}
}

func TestListRawSessions_Filters(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	day := func(d int) time.Time { return time.Date(2026, 6, d, 12, 0, 0, 0, time.UTC) }
	a := uuid.New() // codex, used code-review, day 1
	b := uuid.New() // claude-code, no skill, day 5
	seedSession(t, st, a, "codex", "/proj", day(1), "code-review")
	seedSession(t, st, b, "claude-code", "/proj", day(5), "")

	// Harness filter.
	got, err := st.ListRawSessions(ctx, &RawSessionFilter{Agent: "claude-code"})
	if err != nil {
		t.Fatalf("agent filter: %v", err)
	}
	if len(got) != 1 || got[0].SessionID != b {
		t.Errorf("agent filter = %d sessions, want only b", len(got))
	}

	// Skill filter (matched via the session's spans).
	got, err = st.ListRawSessions(ctx, &RawSessionFilter{Skill: "code-review"})
	if err != nil {
		t.Fatalf("skill filter: %v", err)
	}
	if len(got) != 1 || got[0].SessionID != a {
		t.Errorf("skill filter = %d sessions, want only a", len(got))
	}

	// Date window: only day 1 (excludes day 5).
	until := day(2)
	got, err = st.ListRawSessions(ctx, &RawSessionFilter{Until: &until})
	if err != nil {
		t.Fatalf("until filter: %v", err)
	}
	if len(got) != 1 || got[0].SessionID != a {
		t.Errorf("until filter = %d sessions, want only a (day 1)", len(got))
	}
	since := day(3)
	got, err = st.ListRawSessions(ctx, &RawSessionFilter{Since: &since})
	if err != nil {
		t.Fatalf("since filter: %v", err)
	}
	if len(got) != 1 || got[0].SessionID != b {
		t.Errorf("since filter = %d sessions, want only b (day 5)", len(got))
	}
}

func TestListRawSessions_SkillsOnly(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	a := uuid.New() // used a skill
	b := uuid.New() // skill-less (lingering in the DB)
	seedSession(t, st, a, "codex", "/proj", time.Now().UTC(), "code-review")
	seedSession(t, st, b, "claude-code", "/proj", time.Now().UTC(), "")

	// Default (SkillsOnly false): the DB returns every session, skill-less ones
	// included — the store is the complete record.
	all, err := st.ListRawSessions(ctx, &RawSessionFilter{})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("unfiltered = %d sessions, want 2 (skill-less retained in DB)", len(all))
	}

	// SkillsOnly: skill-bearing sessions only — the skill-less one is hidden.
	got, err := st.ListRawSessions(ctx, &RawSessionFilter{SkillsOnly: true})
	if err != nil {
		t.Fatalf("skills-only filter: %v", err)
	}
	if len(got) != 1 || got[0].SessionID != a {
		t.Errorf("skills-only = %d sessions, want only a (the skill user)", len(got))
	}

	// A specific Skill filter already implies skill-bearing; SkillsOnly is a
	// no-op alongside it and must not change the result.
	got, err = st.ListRawSessions(ctx, &RawSessionFilter{Skill: "code-review", SkillsOnly: true})
	if err != nil {
		t.Fatalf("skill+skills-only filter: %v", err)
	}
	if len(got) != 1 || got[0].SessionID != a {
		t.Errorf("skill+skills-only = %d sessions, want only a", len(got))
	}
}

func TestSkillsForSessions(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	a := uuid.New()
	b := uuid.New()
	seedSession(t, st, a, "codex", "/proj", time.Now().UTC(), "code-review")
	seedSession(t, st, b, "claude-code", "/proj", time.Now().UTC(), "")

	got, err := st.SkillsForSessions(ctx, []string{a.String(), b.String()})
	if err != nil {
		t.Fatalf("SkillsForSessions: %v", err)
	}
	if skills := got[a.String()]; len(skills) != 1 || skills[0] != "code-review" {
		t.Errorf("session a skills = %v, want [code-review]", skills)
	}
	if _, ok := got[b.String()]; ok {
		t.Errorf("session b has no skill span; should be absent from the map, got %v", got[b.String()])
	}
}
