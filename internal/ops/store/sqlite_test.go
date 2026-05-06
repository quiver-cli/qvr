package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/raks097/quiver/internal/ops"
)

// openStore returns a fresh store rooted at t.TempDir. Cleaned up via
// t.Cleanup; safe to call multiple times per test.
func openStore(t *testing.T) Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ops.db")
	s, err := Open(context.Background(), OpenOptions{Path: path})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// mkSession returns a session with sane defaults, ready to Upsert.
func mkSession(t *testing.T, agent string) *ops.Session {
	t.Helper()
	s := ops.NewSession(agent, "sess-"+agent, time.Now().UTC())
	s.WorkingDirectory = "/Users/me/project"
	s.ProjectName = "demo"
	return s
}

// mkEvent builds an event tied to s.
func mkEvent(t *testing.T, s *ops.Session, action ops.ActionType, skill string) *ops.Event {
	t.Helper()
	e := &ops.Event{
		ID:               uuid.New(),
		SessionID:        s.ID,
		AgentSessionID:   s.AgentSessionID,
		Sequence:         1,
		Timestamp:        time.Now().UTC(),
		AgentName:        s.AgentName,
		WorkingDirectory: s.WorkingDirectory,
		SkillName:        skill,
		SkillRegistry:    "team",
		SkillCommit:      "abc123",
		ActionType:       action,
		ResultStatus:     ops.ResultSuccess,
	}
	return e
}

// --- Open / Close ---

func TestOpen_RequiresPath(t *testing.T) {
	_, err := Open(context.Background(), OpenOptions{})
	if err == nil {
		t.Errorf("expected error for missing path")
	}
}

func TestOpen_CreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "subdir", "ops.db")
	s, err := Open(context.Background(), OpenOptions{Path: path})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = s.Close()
}

func TestOpen_AppliesMigrations(t *testing.T) {
	s := openStore(t)
	// If migrations applied, Stats shouldn't error.
	if _, err := s.Stats(context.Background()); err != nil {
		t.Errorf("stats after open: %v", err)
	}
}

func TestClose_Idempotent(t *testing.T) {
	s := openStore(t)
	if err := s.Close(); err != nil {
		t.Errorf("first close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
}

// --- SaveEvent / round-trip ---

func TestSaveEvent_RejectsNil(t *testing.T) {
	s := openStore(t)
	if err := s.SaveEvent(context.Background(), nil); err == nil {
		t.Errorf("expected nil event rejection")
	}
}

func TestSaveEvent_RejectsMissingSessionID(t *testing.T) {
	s := openStore(t)
	e := &ops.Event{ID: uuid.New(), AgentName: "claude", SkillName: "x", ActionType: ops.ActionFileRead, ResultStatus: ops.ResultSuccess, Timestamp: time.Now().UTC()}
	if err := s.SaveEvent(context.Background(), e); err == nil {
		t.Errorf("expected missing session_id rejection")
	}
}

func TestSaveEvent_AssignsIDIfMissing(t *testing.T) {
	s := openStore(t)
	sess := mkSession(t, "claude")
	if err := s.UpsertSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	e := mkEvent(t, sess, ops.ActionFileRead, "x")
	e.ID = uuid.Nil
	if err := s.SaveEvent(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if e.ID == uuid.Nil {
		t.Errorf("expected SaveEvent to assign an ID")
	}
}

func TestSaveEvent_AssignsTimestampIfMissing(t *testing.T) {
	s := openStore(t)
	sess := mkSession(t, "claude")
	_ = s.UpsertSession(context.Background(), sess)
	e := mkEvent(t, sess, ops.ActionFileRead, "x")
	e.Timestamp = time.Time{}
	if err := s.SaveEvent(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if e.Timestamp.IsZero() {
		t.Errorf("expected SaveEvent to assign Timestamp")
	}
}

// TestSaveEvent_RoundTrip_AllPayloadTypes — for every typed payload,
// save + query + decode returns the same fields as went in.
func TestSaveEvent_RoundTrip_AllPayloadTypes(t *testing.T) {
	cases := []struct {
		name string
		set  func(*ops.Event)
	}{
		{"FileRead", func(e *ops.Event) { _ = e.SetPayload(ops.FileReadPayload{Path: "/a", Lines: 10}) }},
		{"FileWrite", func(e *ops.Event) { _ = e.SetPayload(ops.FileWritePayload{Path: "/b", NewString: "x"}) }},
		{"FileDelete", func(e *ops.Event) { _ = e.SetPayload(ops.FileDeletePayload{Path: "/c"}) }},
		{"CommandExec", func(e *ops.Event) {
			_ = e.SetPayload(ops.CommandExecPayload{Command: "ls", Stdout: "a\n", ExitCode: 0})
		}},
		{"ToolUse", func(e *ops.Event) {
			_ = e.SetPayload(ops.ToolUsePayload{Input: map[string]any{"path": "/d"}, Output: "done"})
		}},
		{"NetworkRequest", func(e *ops.Event) {
			_ = e.SetPayload(ops.NetworkRequestPayload{URL: "https://x", Method: "GET"})
		}},
		{"Session", func(e *ops.Event) { _ = e.SetPayload(ops.SessionPayload{ProjectName: "p"}) }},
		{"Notification", func(e *ops.Event) {
			_ = e.SetPayload(ops.NotificationPayload{Title: "t", Message: "m"})
		}},
		{"Subagent", func(e *ops.Event) { _ = e.SetPayload(ops.SubagentPayload{Type: "bot"}) }},
		{"SkillInvoke", func(e *ops.Event) { _ = e.SetPayload(ops.SkillInvokePayload{Origin: "qvr"}) }},
	}

	s := openStore(t)
	sess := mkSession(t, "claude")
	if err := s.UpsertSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := mkEvent(t, sess, ops.ActionFileRead, "skill-"+tc.name)
			e.Sequence = i + 1
			tc.set(e)
			origPayload := string(e.Payload)

			if err := s.SaveEvent(context.Background(), e); err != nil {
				t.Fatalf("save: %v", err)
			}
			// Query back via session filter (narrow match).
			got, err := s.GetEventsBySession(context.Background(), sess.ID)
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			found := false
			for _, r := range got {
				if r.ID == e.ID {
					found = true
					if string(r.Payload) != origPayload {
						t.Errorf("payload round-trip drift:\n  in:  %s\n  out: %s", origPayload, r.Payload)
					}
					if r.SkillName != e.SkillName {
						t.Errorf("skill_name drift: %q vs %q", r.SkillName, e.SkillName)
					}
				}
			}
			if !found {
				t.Errorf("event %s not returned", e.ID)
			}
		})
	}
}

func TestSaveEvent_PreservesUTCTimestamp(t *testing.T) {
	s := openStore(t)
	sess := mkSession(t, "claude")
	_ = s.UpsertSession(context.Background(), sess)

	// Submit in a non-UTC zone; read back must be UTC-equivalent.
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	local := time.Date(2026, 4, 23, 10, 0, 0, 0, loc)
	e := mkEvent(t, sess, ops.ActionFileRead, "x")
	e.Timestamp = local

	if err := s.SaveEvent(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	events, err := s.GetEventsBySession(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !events[0].Timestamp.Equal(local) {
		t.Errorf("timestamp drift: %v vs %v", events[0].Timestamp, local)
	}
	if events[0].Timestamp.Location() != time.UTC {
		t.Errorf("expected UTC location; got %v", events[0].Timestamp.Location())
	}
}

func TestSaveEvent_NullablesRoundTrip(t *testing.T) {
	s := openStore(t)
	sess := mkSession(t, "claude")
	_ = s.UpsertSession(context.Background(), sess)

	e := mkEvent(t, sess, ops.ActionFileRead, "x")
	// Leave agent_version, tool_name, error_message, payload, etc. empty.
	e.AgentVersion = ""
	e.ToolName = ""
	e.ErrorMessage = ""
	e.SubagentID = ""
	e.SubagentType = ""
	e.Payload = nil
	e.RawEvent = nil

	if err := s.SaveEvent(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetEventsBySession(context.Background(), sess.ID)
	if len(got) != 1 {
		t.Fatalf("expected 1 event; got %d", len(got))
	}
	r := got[0]
	if r.AgentVersion != "" || r.ToolName != "" || r.ErrorMessage != "" {
		t.Errorf("expected empty-string round-trip; got AgentVersion=%q, ToolName=%q, ErrorMessage=%q",
			r.AgentVersion, r.ToolName, r.ErrorMessage)
	}
	if r.Payload != nil {
		t.Errorf("expected nil payload; got %s", r.Payload)
	}
}

// --- Session upsert / counter updates ---

func TestUpsertSession_Idempotent(t *testing.T) {
	s := openStore(t)
	sess := mkSession(t, "claude")
	sess.TotalActions = 5
	if err := s.UpsertSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	sess.TotalActions = 7
	if err := s.UpsertSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalActions != 7 {
		t.Errorf("expected counter update to 7; got %d", got.TotalActions)
	}
}

func TestUpsertSession_PreservesSkillsTouched(t *testing.T) {
	s := openStore(t)
	sess := mkSession(t, "claude")
	sess.SkillsTouched = []string{"a", "b", "c"}
	_ = s.UpsertSession(context.Background(), sess)
	got, _ := s.GetSession(context.Background(), sess.ID)
	if len(got.SkillsTouched) != 3 {
		t.Errorf("skills_touched drift; got %v", got.SkillsTouched)
	}
}

func TestGetSession_ReturnsNilOnMissing(t *testing.T) {
	s := openStore(t)
	got, err := s.GetSession(context.Background(), uuid.New())
	if err != nil {
		t.Errorf("unexpected err: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing session")
	}
}

func TestListSessions_FiltersAndLimits(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for i := 0; i < 5; i++ {
		sess := ops.NewSession("claude", fmt.Sprintf("s-%d", i), now.Add(time.Duration(-i)*time.Hour))
		if err := s.UpsertSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}
	// Range picks up only the 3 most recent.
	since := now.Add(-2*time.Hour - time.Minute)
	got, err := s.ListSessions(ctx, &since, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("expected 3 sessions in range; got %d", len(got))
	}
	// Limit respected.
	gotLimited, _ := s.ListSessions(ctx, nil, nil, 2)
	if len(gotLimited) != 2 {
		t.Errorf("limit 2 ignored; got %d", len(gotLimited))
	}
	// Descending order.
	for i := 1; i < len(got); i++ {
		if got[i].StartedAt.After(got[i-1].StartedAt) {
			t.Errorf("expected descending started_at; got %v before %v",
				got[i-1].StartedAt, got[i].StartedAt)
		}
	}
}

// --- QueryEvents — each filter dimension hits the DB ---

func seedCorpus(t *testing.T, s Store) (sess1, sess2 *ops.Session) {
	t.Helper()
	ctx := context.Background()
	sess1 = mkSession(t, "claude")
	sess2 = mkSession(t, "cursor")
	_ = s.UpsertSession(ctx, sess1)
	_ = s.UpsertSession(ctx, sess2)

	base := time.Now().UTC().Add(-24 * time.Hour)
	events := []struct {
		sess   *ops.Session
		skill  string
		action ops.ActionType
		offset time.Duration
		sens   bool
		status ops.ResultStatus
	}{
		{sess1, "alpha", ops.ActionFileRead, 0, false, ops.ResultSuccess},
		{sess1, "alpha", ops.ActionFileWrite, time.Hour, false, ops.ResultSuccess},
		{sess1, "alpha", ops.ActionFileRead, 2 * time.Hour, true, ops.ResultSuccess},
		{sess1, "beta", ops.ActionCommandExec, 3 * time.Hour, false, ops.ResultError},
		{sess2, "alpha", ops.ActionFileRead, 4 * time.Hour, false, ops.ResultSuccess},
		{sess2, "beta", ops.ActionFileWrite, 5 * time.Hour, false, ops.ResultBlocked},
	}
	for i, ev := range events {
		e := mkEvent(t, ev.sess, ev.action, ev.skill)
		e.Timestamp = base.Add(ev.offset)
		e.Sequence = i + 1
		e.IsSensitive = ev.sens
		e.ResultStatus = ev.status
		if ev.action == ops.ActionCommandExec {
			_ = e.SetPayload(ops.CommandExecPayload{Command: "rm -rf /tmp/foo"})
		} else {
			_ = e.SetPayload(ops.FileWritePayload{Path: fmt.Sprintf("/project/%s/file-%d", ev.skill, i)})
		}
		if err := s.SaveEvent(ctx, e); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	return sess1, sess2
}

func TestQuery_Unfiltered(t *testing.T) {
	s := openStore(t)
	seedCorpus(t, s)
	got, err := s.QueryEvents(context.Background(), &EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 {
		t.Errorf("expected 6 events; got %d", len(got))
	}
}

func TestQuery_BySkill(t *testing.T) {
	s := openStore(t)
	seedCorpus(t, s)
	got, err := s.QueryEvents(context.Background(), &EventFilter{Skills: []string{"alpha"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Errorf("expected 4 alpha events; got %d", len(got))
	}
	for _, e := range got {
		if e.SkillName != "alpha" {
			t.Errorf("unexpected skill %q", e.SkillName)
		}
	}
}

func TestQuery_ByAgent(t *testing.T) {
	s := openStore(t)
	seedCorpus(t, s)
	got, _ := s.QueryEvents(context.Background(), &EventFilter{Agents: []string{"cursor"}})
	if len(got) != 2 {
		t.Errorf("expected 2 cursor events; got %d", len(got))
	}
}

func TestQuery_ByAction(t *testing.T) {
	s := openStore(t)
	seedCorpus(t, s)
	got, _ := s.QueryEvents(context.Background(), &EventFilter{Actions: []ops.ActionType{ops.ActionFileRead}})
	if len(got) != 3 {
		t.Errorf("expected 3 file_read events; got %d", len(got))
	}
}

func TestQuery_ByResult(t *testing.T) {
	s := openStore(t)
	seedCorpus(t, s)
	got, _ := s.QueryEvents(context.Background(), &EventFilter{Results: []ops.ResultStatus{ops.ResultError, ops.ResultBlocked}})
	if len(got) != 2 {
		t.Errorf("expected 2 non-success events; got %d", len(got))
	}
}

func TestQuery_BySensitive(t *testing.T) {
	s := openStore(t)
	seedCorpus(t, s)
	trueV := true
	got, _ := s.QueryEvents(context.Background(), &EventFilter{IsSensitive: &trueV})
	if len(got) != 1 {
		t.Errorf("expected 1 sensitive event; got %d", len(got))
	}
}

func TestQuery_BySession(t *testing.T) {
	s := openStore(t)
	sess1, sess2 := seedCorpus(t, s)
	got1, _ := s.QueryEvents(context.Background(), &EventFilter{SessionID: &sess1.ID})
	got2, _ := s.QueryEvents(context.Background(), &EventFilter{SessionID: &sess2.ID})
	if len(got1) != 4 {
		t.Errorf("expected 4 in sess1; got %d", len(got1))
	}
	if len(got2) != 2 {
		t.Errorf("expected 2 in sess2; got %d", len(got2))
	}
}

func TestQuery_BySinceUntil(t *testing.T) {
	s := openStore(t)
	seedCorpus(t, s)
	base := time.Now().UTC().Add(-24 * time.Hour)
	since := base.Add(90 * time.Minute)
	until := base.Add(3*time.Hour + 30*time.Minute)
	got, _ := s.QueryEvents(context.Background(), &EventFilter{Since: &since, Until: &until})
	// Window covers offsets 2h and 3h → 2 events.
	if len(got) != 2 {
		t.Errorf("expected 2 events in window; got %d", len(got))
	}
}

func TestQuery_ByFilePattern(t *testing.T) {
	s := openStore(t)
	seedCorpus(t, s)
	got, _ := s.QueryEvents(context.Background(), &EventFilter{FilePatterns: []string{"*alpha*"}})
	if len(got) == 0 {
		t.Errorf("expected matches on *alpha*; got 0")
	}
	for _, e := range got {
		if !strings.Contains(string(e.Payload), "alpha") {
			t.Errorf("payload %s does not contain alpha", e.Payload)
		}
	}
}

func TestQuery_ByCommandPattern(t *testing.T) {
	s := openStore(t)
	seedCorpus(t, s)
	got, _ := s.QueryEvents(context.Background(), &EventFilter{CommandPatterns: []string{"*rm*"}})
	if len(got) != 1 {
		t.Errorf("expected 1 command_exec match; got %d", len(got))
	}
}

func TestQuery_Combined(t *testing.T) {
	s := openStore(t)
	sess1, _ := seedCorpus(t, s)
	got, _ := s.QueryEvents(context.Background(), &EventFilter{
		SessionID: &sess1.ID,
		Actions:   []ops.ActionType{ops.ActionFileRead},
	})
	if len(got) != 2 {
		t.Errorf("expected 2 (sess1 ∩ file_read); got %d", len(got))
	}
}

func TestQuery_OrderedDescByTimestamp(t *testing.T) {
	s := openStore(t)
	seedCorpus(t, s)
	got, _ := s.QueryEvents(context.Background(), &EventFilter{})
	for i := 1; i < len(got); i++ {
		if got[i].Timestamp.After(got[i-1].Timestamp) {
			t.Errorf("events not descending by timestamp at index %d", i)
		}
	}
}

func TestQuery_LimitRespected(t *testing.T) {
	s := openStore(t)
	seedCorpus(t, s)
	got, _ := s.QueryEvents(context.Background(), &EventFilter{Limit: 3})
	if len(got) != 3 {
		t.Errorf("expected 3 with Limit=3; got %d", len(got))
	}
}

// --- Cursor pagination ---

func TestQuery_CursorPagination_NoGapsNoDuplicates(t *testing.T) {
	s := openStore(t)
	sess := mkSession(t, "claude")
	_ = s.UpsertSession(context.Background(), sess)

	// Insert 25 events; paginate 10 at a time.
	base := time.Now().UTC()
	for i := 0; i < 25; i++ {
		e := mkEvent(t, sess, ops.ActionFileRead, "x")
		e.Timestamp = base.Add(time.Duration(-i) * time.Second)
		e.Sequence = i + 1
		if err := s.SaveEvent(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[uuid.UUID]int{}
	var cursor *Cursor
	for page := 0; page < 5; page++ {
		got, err := s.QueryEvents(context.Background(), &EventFilter{Limit: 10, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) == 0 {
			break
		}
		for _, e := range got {
			seen[e.ID]++
		}
		cursor = &Cursor{Timestamp: got[len(got)-1].Timestamp, ID: got[len(got)-1].ID}
	}
	if len(seen) != 25 {
		t.Errorf("expected 25 distinct events across pages; got %d", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("event %s returned %d times", id, n)
		}
	}
}

func TestStreamEvents_VisitsAll(t *testing.T) {
	s := openStore(t)
	sess := mkSession(t, "claude")
	_ = s.UpsertSession(context.Background(), sess)
	for i := 0; i < 13; i++ {
		e := mkEvent(t, sess, ops.ActionFileRead, "x")
		e.Timestamp = time.Now().UTC().Add(time.Duration(-i) * time.Second)
		e.Sequence = i + 1
		_ = s.SaveEvent(context.Background(), e)
	}
	count := 0
	err := s.StreamEvents(context.Background(), &EventFilter{Limit: 5}, func(*ops.Event) error {
		count++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 13 {
		t.Errorf("expected 13 visits; got %d", count)
	}
}

func TestStreamEvents_StopsOnFnError(t *testing.T) {
	s := openStore(t)
	sess := mkSession(t, "claude")
	_ = s.UpsertSession(context.Background(), sess)
	for i := 0; i < 10; i++ {
		e := mkEvent(t, sess, ops.ActionFileRead, "x")
		e.Timestamp = time.Now().UTC().Add(time.Duration(-i) * time.Second)
		e.Sequence = i + 1
		_ = s.SaveEvent(context.Background(), e)
	}
	count := 0
	stopErr := fmt.Errorf("stop")
	err := s.StreamEvents(context.Background(), &EventFilter{Limit: 3}, func(*ops.Event) error {
		count++
		if count == 2 {
			return stopErr
		}
		return nil
	})
	if err != stopErr {
		t.Errorf("expected stop error; got %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 visits before stop; got %d", count)
	}
}

// --- SkillVersion ---

func TestUpsertSkillVersion_Immutable(t *testing.T) {
	s := openStore(t)
	sv := &ops.SkillVersion{
		Registry: "team", Name: "x", Commit: "abc",
		Branch: "main", FirstSeenAt: time.Now().UTC(),
	}
	orig := sv.FirstSeenAt
	if err := s.UpsertSkillVersion(context.Background(), sv); err != nil {
		t.Fatal(err)
	}
	// Second upsert with different FirstSeenAt: should not overwrite
	// (ON CONFLICT DO NOTHING).
	sv2 := *sv
	sv2.FirstSeenAt = orig.Add(time.Hour)
	if err := s.UpsertSkillVersion(context.Background(), &sv2); err != nil {
		t.Fatal(err)
	}
	// We don't expose a read for skill_versions yet; query via stats.
	stats, _ := s.Stats(context.Background())
	if stats.SkillVersionCount != 1 {
		t.Errorf("expected 1 row; got %d", stats.SkillVersionCount)
	}
}

// --- Retention ---

func TestDeleteEventsBefore_ReturnsCount(t *testing.T) {
	s := openStore(t)
	sess := mkSession(t, "claude")
	_ = s.UpsertSession(context.Background(), sess)
	base := time.Now().UTC()
	for i := 0; i < 5; i++ {
		e := mkEvent(t, sess, ops.ActionFileRead, "x")
		e.Timestamp = base.Add(time.Duration(-i) * 24 * time.Hour)
		e.Sequence = i + 1
		_ = s.SaveEvent(context.Background(), e)
	}
	cutoff := base.Add(-2*24*time.Hour - time.Minute)
	n, err := s.DeleteEventsBefore(context.Background(), cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("expected 2 deleted; got %d", n)
	}
	// Sessions untouched.
	got, _ := s.GetSession(context.Background(), sess.ID)
	if got == nil {
		t.Errorf("sessions should not be deleted by event retention")
	}
}

func TestDeleteEventsBefore_Idempotent(t *testing.T) {
	s := openStore(t)
	cutoff := time.Now().UTC()
	n1, _ := s.DeleteEventsBefore(context.Background(), cutoff)
	n2, _ := s.DeleteEventsBefore(context.Background(), cutoff)
	if n1 != 0 || n2 != 0 {
		t.Errorf("expected 0, 0; got %d, %d", n1, n2)
	}
}

// --- SelfAudit ---

func TestAppendSelfAudit_RoundTrip(t *testing.T) {
	s := openStore(t)
	entry := &SelfAudit{
		Action:   ActionHookError,
		Actor:    "claude",
		Result:   ResultAudit_Error,
		ErrorMsg: "parse failed",
		Details:  map[string]any{"raw_size": 123},
	}
	if err := s.AppendSelfAudit(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	if entry.ID == uuid.Nil {
		t.Errorf("expected ID assigned")
	}
	stats, _ := s.Stats(context.Background())
	if stats.SelfAuditCount != 1 {
		t.Errorf("expected 1 self_audit row; got %d", stats.SelfAuditCount)
	}
}

func TestAppendSelfAudit_AllKnownActions(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	for _, action := range []string{
		ActionHookError, ActionUnattributedDrop, ActionPurge,
		ActionConfigChange, ActionAdapterInstall, ActionAdapterUninstall,
	} {
		err := s.AppendSelfAudit(ctx, &SelfAudit{Action: action, Result: ResultAudit_Success})
		if err != nil {
			t.Errorf("append action=%q: %v", action, err)
		}
	}
	stats, _ := s.Stats(ctx)
	if stats.SelfAuditCount != 6 {
		t.Errorf("expected 6 self_audit rows; got %d", stats.SelfAuditCount)
	}
}

// --- Stats ---

func TestStats_Accuracy(t *testing.T) {
	s := openStore(t)
	_, _ = seedCorpus(t, s)
	stats, err := s.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.EventCount != 6 {
		t.Errorf("EventCount=%d want 6", stats.EventCount)
	}
	if stats.SessionCount != 2 {
		t.Errorf("SessionCount=%d want 2", stats.SessionCount)
	}
	if stats.SensitiveCount != 1 {
		t.Errorf("SensitiveCount=%d want 1", stats.SensitiveCount)
	}
	if stats.DBSizeBytes <= 0 {
		t.Errorf("expected DBSizeBytes>0; got %d", stats.DBSizeBytes)
	}
	if stats.OldestEvent == nil || stats.NewestEvent == nil {
		t.Errorf("expected oldest/newest populated")
	}
}

func TestStats_EmptyDB(t *testing.T) {
	s := openStore(t)
	stats, err := s.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.EventCount != 0 {
		t.Errorf("expected 0 events in fresh DB")
	}
	if stats.OldestEvent != nil {
		t.Errorf("expected nil OldestEvent on empty DB")
	}
}

// --- Concurrency ---

func TestSaveEvent_ConcurrentWriters(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	sess := mkSession(t, "claude")
	_ = s.UpsertSession(ctx, sess)

	const N = 50
	const workers = 5
	var wg sync.WaitGroup
	errs := make(chan error, workers)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < N; i++ {
				e := mkEvent(t, sess, ops.ActionFileRead, fmt.Sprintf("skill-%d", w))
				e.Sequence = w*N + i
				e.Timestamp = time.Now().UTC().Add(time.Duration(i) * time.Microsecond)
				if err := s.SaveEvent(ctx, e); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent save: %v", err)
	}

	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("stats after concurrent writes: %v", err)
	}
	if stats.EventCount != int64(N*workers) {
		t.Errorf("expected %d events; got %d", N*workers, stats.EventCount)
	}
}

// --- Property: save+query returns equivalent payload JSON ---

func TestProperty_PayloadJSONRoundTrip(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	sess := mkSession(t, "claude")
	_ = s.UpsertSession(ctx, sess)

	cases := []struct {
		name string
		p    any
	}{
		{"nested", map[string]any{"a": 1, "b": []any{"x", "y"}, "c": map[string]any{"d": "e"}}},
		{"unicode", map[string]any{"msg": "προεκτεταμένο 日本語"}},
		{"large_string", map[string]any{"payload": strings.Repeat("x", 10_000)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := mkEvent(t, sess, ops.ActionFileRead, "x")
			_ = e.SetPayload(tc.p)
			orig := string(e.Payload)
			if err := s.SaveEvent(ctx, e); err != nil {
				t.Fatal(err)
			}
			got, _ := s.GetEventsBySession(ctx, sess.ID)
			var found *ops.Event
			for _, r := range got {
				if r.ID == e.ID {
					found = r
				}
			}
			if found == nil {
				t.Fatal("event not found")
			}
			// JSON semantic equality (strings may reorder keys).
			var a, b any
			if err := json.Unmarshal([]byte(orig), &a); err != nil {
				t.Fatalf("unmarshal input %s: %v", orig, err)
			}
			if err := json.Unmarshal(found.Payload, &b); err != nil {
				t.Fatalf("unmarshal stored %s: %v", found.Payload, err)
			}
			equal, err := jsonDeepEqual(a, b)
			if err != nil {
				t.Fatalf("jsonDeepEqual: %v", err)
			}
			if !equal {
				t.Errorf("JSON drift:\n  in:  %s\n  out: %s", orig, found.Payload)
			}
		})
	}
}

// jsonDeepEqual compares two json-decoded values ignoring map key order.
// Re-marshals and byte-compares; returns an error if either re-marshal
// fails so callers don't mistake a marshal failure for inequality.
func jsonDeepEqual(a, b any) (bool, error) {
	ja, err := json.Marshal(a)
	if err != nil {
		return false, fmt.Errorf("marshal a: %w", err)
	}
	jb, err := json.Marshal(b)
	if err != nil {
		return false, fmt.Errorf("marshal b: %w", err)
	}
	return string(ja) == string(jb), nil
}
