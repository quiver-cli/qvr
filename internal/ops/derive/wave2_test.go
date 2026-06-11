package derive_test

import (
	"testing"

	"github.com/astra-sh/qvr/internal/ops/derive"
)

// TestCursorDerive exercises the cursor transcript shape: top-level role,
// Anthropic-style content blocks, no in-file timestamps, <user_query> tags.
func TestCursorDerive(t *testing.T) {
	rows := agentRows("cursor",
		`{"role":"user","message":{"content":[{"type":"text","text":"<user_query>review my diff</user_query>"}]}}`,
		`{"role":"assistant","message":{"content":[{"type":"thinking","thinking":"plan"},{"type":"text","text":"loading"},{"type":"tool_use","id":"tc1","name":"run_terminal_cmd","input":{"command":"cat .agents/skills/code-review/SKILL.md"}}]}}`,
		`{"role":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tc1","content":"# skill body"}]}}`,
		`{"role":"assistant","message":{"content":[{"type":"text","text":"done"}]}}`,
	)
	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	assertSkillTurn(t, d)
	if d.Meta.Title != "review my diff" {
		t.Errorf("title = %q, want tags stripped", d.Meta.Title)
	}
	// No in-file timestamps: capture times must stand in for day bucketing.
	if d.Meta.StartedMs == 0 {
		t.Error("meta started_ms = 0 — capture-time fallback missing")
	}
}

// TestGeminiDerive_WrapperDoc exercises the wrapper-object document form:
// messages array, gemini/model roles, toolCalls with inline functionResponse.
func TestGeminiDerive_WrapperDoc(t *testing.T) {
	doc := `{
	  "sessionId":"g-1","model":"gemini-pro-x","startTime":"2026-06-03T10:00:00.000Z",
	  "messages":[
	    {"role":"user","timestamp":"2026-06-03T10:00:01.000Z","content":"review my diff"},
	    {"role":"gemini","timestamp":"2026-06-03T10:00:02.000Z","parts":[{"text":"loading"}],
	     "toolCalls":[{"name":"run_shell_command","id":"tc1",
	       "args":{"command":"cat .agents/skills/code-review/SKILL.md"},
	       "result":[{"functionResponse":{"response":{"output":"# skill body"}}}]}]},
	    {"role":"model","timestamp":"2026-06-03T10:00:03.000Z","parts":[{"text":"done"}]}
	  ]
	}`
	d, err := derive.DeriveSession(agentRows("gemini", doc))
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	assertSkillTurn(t, d)
	if d.Meta.Model != "gemini-pro-x" {
		t.Errorf("meta model = %q", d.Meta.Model)
	}
	if d.Meta.Title != "review my diff" {
		t.Errorf("meta title = %q", d.Meta.Title)
	}
}

// TestGeminiDerive_FlatArray exercises the flat-array form with snake_case
// tool calls and epoch-second timestamps.
func TestGeminiDerive_FlatArray(t *testing.T) {
	doc := `[
	  {"type":"user","ts":1780480801,"text":"review my diff"},
	  {"type":"model","ts":1780480802,"content":[{"text":"loading"}],
	   "tool_calls":[{"tool":"shell","id":"tc1","input":{"command":"cat .agents/skills/code-review/SKILL.md"},"output":"# skill body"}]}
	]`
	d, err := derive.DeriveSession(agentRows("gemini", doc))
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	assertSkillTurn(t, d)
	if d.Meta.Title != "review my diff" {
		t.Errorf("meta title = %q", d.Meta.Title)
	}
	if d.Meta.StartedMs != 1780480801000 {
		t.Errorf("epoch-second timestamp not normalized: %d", d.Meta.StartedMs)
	}
}
