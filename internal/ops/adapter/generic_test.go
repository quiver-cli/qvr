package generic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/raks097/quiver/internal/ops"
)

func TestAdapter_Name(t *testing.T) {
	if got := (&Adapter{}).Name(); got != "generic" {
		t.Errorf("Name=%q want %q", got, "generic")
	}
}

func TestAdapter_RejectsEmptyStdin(t *testing.T) {
	_, err := (&Adapter{}).ParseEvent(context.Background(), "PostToolUse", nil)
	if err == nil {
		t.Errorf("expected error for empty stdin")
	}
}

func TestAdapter_RejectsInvalidJSON(t *testing.T) {
	_, err := (&Adapter{}).ParseEvent(context.Background(), "PostToolUse", []byte("{not json"))
	if err == nil {
		t.Errorf("expected JSON parse error")
	}
}

func TestAdapter_FillsMissingID(t *testing.T) {
	raw := []byte(`{"agent_name":"claude","agent_session_id":"sess-abc","action_type":"file_read"}`)
	e, err := (&Adapter{}).ParseEvent(context.Background(), "PostToolUse", raw)
	if err != nil {
		t.Fatal(err)
	}
	if e.ID == uuid.Nil {
		t.Errorf("expected generated ID")
	}
}

func TestAdapter_FillsMissingTimestamp(t *testing.T) {
	raw := []byte(`{"agent_name":"claude","agent_session_id":"s","action_type":"file_read"}`)
	before := time.Now().UTC().Add(-time.Second)
	e, err := (&Adapter{}).ParseEvent(context.Background(), "PostToolUse", raw)
	if err != nil {
		t.Fatal(err)
	}
	if e.Timestamp.Before(before) {
		t.Errorf("expected recent timestamp; got %v", e.Timestamp)
	}
}

func TestAdapter_DeterministicSessionIDFromAgentSessionID(t *testing.T) {
	raw := []byte(`{"agent_name":"claude","agent_session_id":"stable-key","action_type":"file_read"}`)
	a, _ := (&Adapter{}).ParseEvent(context.Background(), "x", raw)
	b, _ := (&Adapter{}).ParseEvent(context.Background(), "x", raw)
	if a.SessionID != b.SessionID {
		t.Errorf("expected deterministic SessionID; got %s vs %s", a.SessionID, b.SessionID)
	}
	// Verify it matches ops.NewSession's derivation.
	expected := uuid.NewSHA1(uuid.NameSpaceOID, []byte("stable-key"))
	if a.SessionID != expected {
		t.Errorf("mismatch with ops.NewSession derivation: %s vs %s", a.SessionID, expected)
	}
}

func TestAdapter_RejectsMissingSessionIDAndAgentSessionID(t *testing.T) {
	raw := []byte(`{"agent_name":"claude","action_type":"file_read"}`)
	_, err := (&Adapter{}).ParseEvent(context.Background(), "x", raw)
	if err == nil {
		t.Errorf("expected rejection")
	}
}

func TestAdapter_PreservesExplicitFields(t *testing.T) {
	sessID := uuid.New()
	raw, _ := json.Marshal(map[string]any{
		"id":                uuid.New().String(),
		"session_id":        sessID.String(),
		"agent_session_id":  "s",
		"agent_name":        "claude",
		"action_type":       string(ops.ActionFileRead),
		"result_status":     string(ops.ResultError),
		"working_directory": "/abs/path",
	})
	e, err := (&Adapter{}).ParseEvent(context.Background(), "x", raw)
	if err != nil {
		t.Fatal(err)
	}
	if e.SessionID != sessID {
		t.Errorf("SessionID overridden")
	}
	if e.ResultStatus != ops.ResultError {
		t.Errorf("ResultStatus overridden")
	}
	if e.WorkingDirectory != "/abs/path" {
		t.Errorf("WorkingDirectory dropped")
	}
}

func TestAdapter_ActionTypeFromHookType(t *testing.T) {
	cases := map[string]ops.ActionType{
		"PreToolUse":    ops.ActionToolUse,
		"PostToolUse":   ops.ActionToolUse,
		"SessionStart":  ops.ActionSessionStart,
		"SessionEnd":    ops.ActionSessionEnd,
		"Stop":          ops.ActionSessionEnd,
		"SubagentStart": ops.ActionSubagentStart,
		"SubagentStop":  ops.ActionSubagentStop,
		"Notification":  ops.ActionNotification,
		"UnknownThing":  ops.ActionUnknown,
	}
	for hookType, wantAction := range cases {
		t.Run(hookType, func(t *testing.T) {
			raw := []byte(`{"agent_name":"claude","agent_session_id":"s"}`)
			e, err := (&Adapter{}).ParseEvent(context.Background(), hookType, raw)
			if err != nil {
				t.Fatal(err)
			}
			if e.ActionType != wantAction {
				t.Errorf("hook=%q: action=%q want %q", hookType, e.ActionType, wantAction)
			}
		})
	}
}

func TestAdapter_ResultStatusDefaultsSuccess(t *testing.T) {
	raw := []byte(`{"agent_name":"claude","agent_session_id":"s","action_type":"file_read"}`)
	e, _ := (&Adapter{}).ParseEvent(context.Background(), "x", raw)
	if e.ResultStatus != ops.ResultSuccess {
		t.Errorf("expected ResultSuccess; got %q", e.ResultStatus)
	}
}

func TestAdapter_PreservesRawEventBytes(t *testing.T) {
	raw := []byte(`{"agent_name":"claude","agent_session_id":"s","action_type":"file_read"}`)
	e, _ := (&Adapter{}).ParseEvent(context.Background(), "x", raw)
	if !strings.Contains(string(e.RawEvent), `"agent_name":"claude"`) {
		t.Errorf("RawEvent missing original bytes: %s", e.RawEvent)
	}
}

func TestAdapter_RawEventIndependentBuffer(t *testing.T) {
	// Mutating the caller's byte slice must not affect the stored RawEvent.
	raw := []byte(`{"agent_name":"claude","agent_session_id":"s","action_type":"file_read"}`)
	e, _ := (&Adapter{}).ParseEvent(context.Background(), "x", raw)
	original := string(e.RawEvent)
	for i := range raw {
		raw[i] = 'X'
	}
	if string(e.RawEvent) != original {
		t.Errorf("RawEvent shares backing array with caller slice")
	}
}

func TestAdapter_RegisteredAtInit(t *testing.T) {
	got, err := ops.GetAdapter("generic")
	if err != nil {
		t.Fatalf("generic not registered: %v", err)
	}
	if got.Name() != "generic" {
		t.Errorf("wrong adapter")
	}
}
