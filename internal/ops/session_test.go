package ops

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewSession_DeterministicFromAgentSessionID(t *testing.T) {
	start := time.Now()
	a := NewSession("claude", "sess-abc", start)
	b := NewSession("claude", "sess-abc", start)
	if a.ID != b.ID {
		t.Errorf("expected deterministic ID for same agent_session_id; got %s vs %s", a.ID, b.ID)
	}
}

func TestNewSession_DifferentAgentSessionIDsDiffer(t *testing.T) {
	start := time.Now()
	a := NewSession("claude", "sess-abc", start)
	b := NewSession("claude", "sess-def", start)
	if a.ID == b.ID {
		t.Errorf("expected different IDs; got %s == %s", a.ID, b.ID)
	}
}

func TestNewSession_EmptyAgentSessionIDRandom(t *testing.T) {
	start := time.Now()
	a := NewSession("claude", "", start)
	b := NewSession("claude", "", start)
	if a.ID == uuid.Nil {
		t.Errorf("expected random ID, got Nil")
	}
	if a.ID == b.ID {
		t.Errorf("expected random IDs to differ; got %s == %s", a.ID, b.ID)
	}
}

func TestSession_RecordEvent_IncrementsCounters(t *testing.T) {
	s := NewSession("claude", "s1", time.Now())
	s.RecordEvent(&Event{ActionType: ActionFileRead, ResultStatus: ResultSuccess, SkillName: "foo"})
	s.RecordEvent(&Event{ActionType: ActionFileWrite, ResultStatus: ResultSuccess, SkillName: "foo"})
	s.RecordEvent(&Event{ActionType: ActionCommandExec, ResultStatus: ResultError, SkillName: "bar"})
	s.RecordEvent(&Event{ActionType: ActionFileRead, ResultStatus: ResultBlocked, SkillName: "bar", IsSensitive: true})

	if s.TotalActions != 4 {
		t.Errorf("TotalActions=%d want 4", s.TotalActions)
	}
	if s.FilesRead != 2 {
		t.Errorf("FilesRead=%d want 2", s.FilesRead)
	}
	if s.FilesWritten != 1 {
		t.Errorf("FilesWritten=%d want 1", s.FilesWritten)
	}
	if s.CommandsExecuted != 1 {
		t.Errorf("CommandsExecuted=%d want 1", s.CommandsExecuted)
	}
	if s.Errors != 1 {
		t.Errorf("Errors=%d want 1", s.Errors)
	}
	if s.BlockedActions != 1 {
		t.Errorf("BlockedActions=%d want 1", s.BlockedActions)
	}
	if s.SensitiveActions != 1 {
		t.Errorf("SensitiveActions=%d want 1", s.SensitiveActions)
	}
}

func TestSession_AddSkillTouched_Dedupes(t *testing.T) {
	s := NewSession("claude", "s1", time.Now())
	s.AddSkillTouched("foo")
	s.AddSkillTouched("bar")
	s.AddSkillTouched("foo")
	s.AddSkillTouched("")
	if len(s.SkillsTouched) != 2 {
		t.Errorf("expected 2 skills; got %v", s.SkillsTouched)
	}
}

func TestSession_RecordEvent_PopulatesSkillsTouched(t *testing.T) {
	s := NewSession("claude", "s1", time.Now())
	s.RecordEvent(&Event{ActionType: ActionFileRead, SkillName: "foo"})
	s.RecordEvent(&Event{ActionType: ActionFileWrite, SkillName: "foo"})
	s.RecordEvent(&Event{ActionType: ActionFileRead, SkillName: "bar"})
	if len(s.SkillsTouched) != 2 {
		t.Errorf("expected skills_touched=[foo,bar]; got %v", s.SkillsTouched)
	}
}

func TestSession_EndSetsTimestamp(t *testing.T) {
	start := time.Now()
	s := NewSession("claude", "s1", start)
	end := start.Add(5 * time.Minute)
	s.End(end, "user-exit")
	if s.EndedAt == nil || !s.EndedAt.Equal(end) {
		t.Errorf("expected EndedAt=%v; got %v", end, s.EndedAt)
	}
	if got, want := s.Duration(), 5*time.Minute; got != want {
		t.Errorf("duration %v want %v", got, want)
	}
}

func TestSession_DurationOpenIsZero(t *testing.T) {
	s := NewSession("claude", "s1", time.Now())
	if s.Duration() != 0 {
		t.Errorf("open session duration should be 0")
	}
}

func TestSession_NilSafety(t *testing.T) {
	var s *Session
	s.RecordEvent(&Event{ActionType: ActionFileRead})
	s.AddSkillTouched("foo")
	s.End(time.Now(), "")
	if s.Duration() != 0 {
		t.Errorf("nil session duration should be 0")
	}
}
