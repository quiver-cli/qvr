package ops

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/raks097/quiver/internal/privacy"
)

// Compile-time assertion: *Event satisfies privacy.Event. This is the
// load-bearing integration point between ops and privacy.
var _ privacy.Event = (*Event)(nil)

func sampleEvent(t *testing.T) *Event {
	t.Helper()
	e := &Event{
		ID:               uuid.New(),
		SessionID:        uuid.New(),
		AgentSessionID:   "session-abc",
		Sequence:         1,
		Timestamp:        time.Date(2026, 4, 23, 17, 0, 0, 0, time.UTC),
		AgentName:        "claude",
		WorkingDirectory: "/Users/me/project",
		SkillName:        "code-review",
		SkillRegistry:    "team",
		SkillCommit:      "abc123",
		ActionType:       ActionFileWrite,
		ResultStatus:     ResultSuccess,
	}
	if err := e.SetPayload(FileWritePayload{
		Path:      "code-review/SKILL.md",
		NewString: "some new content",
		OldString: "some old content",
	}); err != nil {
		t.Fatalf("SetPayload: %v", err)
	}
	return e
}

func TestEvent_MarshalJSONIncludesSchema(t *testing.T) {
	e := sampleEvent(t)
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if schema, ok := envelope["$schema"].(string); !ok || schema != EventSchemaURL {
		t.Errorf("expected $schema=%q; got %q", EventSchemaURL, envelope["$schema"])
	}
	// Round-trip fidelity on the core fields.
	if envelope["agent_name"] != "claude" {
		t.Errorf("expected agent_name=claude; got %v", envelope["agent_name"])
	}
	if envelope["action_type"] != string(ActionFileWrite) {
		t.Errorf("unexpected action_type: %v", envelope["action_type"])
	}
}

func TestEvent_SetPayloadRoundTripAllTypes(t *testing.T) {
	cases := []struct {
		name string
		in   any
	}{
		{"FileRead", FileReadPayload{Path: "/x", Lines: 42}},
		{"FileWrite", FileWritePayload{Path: "/x", NewString: "abc", Created: true}},
		{"FileDelete", FileDeletePayload{Path: "/x"}},
		{"CommandExec", CommandExecPayload{Command: "ls", Stdout: "a.txt\n", ExitCode: 0}},
		{"ToolUse", ToolUsePayload{Input: map[string]any{"path": "/x"}, Output: "done"}},
		{"NetworkRequest", NetworkRequestPayload{URL: "https://example.com", Method: "GET", StatusCode: 200}},
		{"Session", SessionPayload{ProjectName: "demo", Reason: "user-end"}},
		{"Notification", NotificationPayload{Title: "done", Message: "ok"}},
		{"Subagent", SubagentPayload{Type: "review-bot", Prompt: "check this"}},
		{"SkillInvoke", SkillInvokePayload{Origin: "qvr-install", Ref: "v1.2.0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &Event{}
			if err := e.SetPayload(tc.in); err != nil {
				t.Fatalf("SetPayload: %v", err)
			}
			// Decode back into the same concrete type.
			switch want := tc.in.(type) {
			case FileReadPayload:
				var got FileReadPayload
				if err := e.DecodePayload(&got); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if got != want {
					t.Errorf("round-trip mismatch:\n got:  %#v\n want: %#v", got, want)
				}
			// For types with non-comparable fields (ToolUsePayload
			// has a map), fall back to JSON equality.
			default:
				wantJSON, _ := json.Marshal(tc.in)
				if string(e.Payload) != string(wantJSON) {
					t.Errorf("payload round-trip mismatch:\n got:  %s\n want: %s", e.Payload, wantJSON)
				}
			}
		})
	}
}

func TestEvent_DecodePayload_EmptyReturnsSentinel(t *testing.T) {
	e := &Event{}
	var fr FileReadPayload
	err := e.DecodePayload(&fr)
	if !errors.Is(err, ErrNoPayload) {
		t.Errorf("expected ErrNoPayload; got %v", err)
	}
}

func TestEvent_SetPayloadNilClears(t *testing.T) {
	e := &Event{}
	_ = e.SetPayload(FileReadPayload{Path: "/a"})
	if len(e.Payload) == 0 {
		t.Fatalf("setup failed")
	}
	if err := e.SetPayload(nil); err != nil {
		t.Fatalf("nil setpayload: %v", err)
	}
	if e.Payload != nil {
		t.Errorf("expected Payload cleared; got %s", e.Payload)
	}
}

// --- privacy.Event implementation ---

func TestEvent_GetPaths_FileWrite(t *testing.T) {
	e := sampleEvent(t)
	paths := e.GetPaths()
	if len(paths) == 0 || paths[0] != "code-review/SKILL.md" {
		t.Errorf("expected path in GetPaths; got %v", paths)
	}
}

func TestEvent_GetPaths_ToolUseMultipleKeys(t *testing.T) {
	e := &Event{ActionType: ActionToolUse}
	_ = e.SetPayload(ToolUsePayload{Input: map[string]any{
		"file_path": "/tmp/x",
	}})
	paths := e.GetPaths()
	if !containsStr(paths, "/tmp/x") {
		t.Errorf("expected /tmp/x in paths; got %v", paths)
	}
}

func TestEvent_GetStringFields_CoversPayload(t *testing.T) {
	e := &Event{
		DiffContent:  "a diff",
		ErrorMessage: "boom",
		ActionType:   ActionCommandExec,
	}
	_ = e.SetPayload(CommandExecPayload{
		Command: "ls -la",
		Stdout:  "abc",
		Stderr:  "",
	})
	fields := e.GetStringFields()
	if fields["diff_content"] != "a diff" {
		t.Errorf("missing diff_content")
	}
	if fields["error_message"] != "boom" {
		t.Errorf("missing error_message")
	}
	if fields["payload.command"] != "ls -la" {
		t.Errorf("expected payload.command; got %v", fields)
	}
	if fields["payload.stdout"] != "abc" {
		t.Errorf("expected payload.stdout; got %v", fields)
	}
	// Empty fields must not appear.
	if _, ok := fields["payload.stderr"]; ok {
		t.Errorf("empty field should not appear")
	}
}

func TestEvent_StripContent_ClearsContentPreservesMetadata(t *testing.T) {
	e := &Event{
		DiffContent:      "sensitive",
		RawEvent:         json.RawMessage(`{"x":"y"}`),
		ActionType:       ActionFileWrite,
		SkillName:        "code-review",
		Timestamp:        time.Now(),
		WorkingDirectory: "/a",
	}
	_ = e.SetPayload(FileWritePayload{
		Path:      "SKILL.md",
		NewString: "secret bytes",
		OldString: "previous",
	})
	e.StripContent()

	if e.DiffContent != "" {
		t.Errorf("expected DiffContent cleared")
	}
	if e.RawEvent != nil {
		t.Errorf("expected RawEvent cleared")
	}
	// Path must survive.
	var after FileWritePayload
	if err := e.DecodePayload(&after); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if after.Path != "SKILL.md" {
		t.Errorf("expected Path preserved; got %q", after.Path)
	}
	if after.NewString != "" {
		t.Errorf("expected NewString stripped; got %q", after.NewString)
	}
	if after.OldString != "" {
		t.Errorf("expected OldString stripped; got %q", after.OldString)
	}
	// Metadata intact.
	if e.SkillName != "code-review" {
		t.Errorf("attribution should survive stripping")
	}
}

func TestEvent_StripContent_Idempotent(t *testing.T) {
	e := sampleEvent(t)
	e.StripContent()
	beforePayload := string(e.Payload)
	e.StripContent()
	if string(e.Payload) != beforePayload {
		t.Errorf("second strip mutated already-stripped event:\n 1st: %s\n 2nd: %s", beforePayload, e.Payload)
	}
}

func TestEvent_ApplyRedactions_TopLevel(t *testing.T) {
	e := &Event{
		DiffContent:  "password=hunter2",
		ErrorMessage: "bearer leaked",
	}
	e.ApplyRedactions(map[string]string{
		"diff_content":  "[REDACTED]",
		"error_message": "bearer [REDACTED]",
	})
	if e.DiffContent != "[REDACTED]" {
		t.Errorf("diff_content not redacted: %q", e.DiffContent)
	}
	if e.ErrorMessage != "bearer [REDACTED]" {
		t.Errorf("error_message not redacted: %q", e.ErrorMessage)
	}
}

func TestEvent_ApplyRedactions_PayloadField(t *testing.T) {
	e := &Event{ActionType: ActionCommandExec}
	_ = e.SetPayload(CommandExecPayload{
		Command: "curl -H 'Authorization: Bearer abc.def'",
		Stdout:  "output",
	})
	e.ApplyRedactions(map[string]string{
		"payload.command": "curl -H 'Authorization: Bearer [REDACTED]'",
	})
	var p CommandExecPayload
	if err := e.DecodePayload(&p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(p.Command, "[REDACTED]") {
		t.Errorf("command not redacted: %q", p.Command)
	}
	if p.Stdout != "output" {
		t.Errorf("non-redacted field mutated: %q", p.Stdout)
	}
}

func TestEvent_ApplyRedactions_IgnoresUnknownKeys(t *testing.T) {
	e := sampleEvent(t)
	before := string(e.Payload)
	e.ApplyRedactions(map[string]string{"unknown_field": "x"})
	if string(e.Payload) != before {
		t.Errorf("unknown key should not mutate payload")
	}
}

// --- helpers ---

func containsStr(xs []string, target string) bool {
	for _, x := range xs {
		if x == target {
			return true
		}
	}
	return false
}
