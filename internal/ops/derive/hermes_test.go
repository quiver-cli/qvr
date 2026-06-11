package derive_test

import (
	"testing"

	"github.com/astra-sh/qvr/internal/ops/derive"
)

// TestHermesDerive exercises the session-document shape: OpenAI-style
// function calls with results in separate tool-role messages.
func TestHermesDerive(t *testing.T) {
	doc := `{
	  "session_id":"h-1","model":"model-h","platform":"cli",
	  "session_start":"2026-06-03T10:00:00Z","last_updated":"2026-06-03T10:05:00Z",
	  "cwd":"/tmp/proj",
	  "messages":[
	    {"role":"user","content":"review my diff"},
	    {"role":"assistant","content":"loading","tool_calls":[
	      {"id":"tc1","function":{"name":"terminal","arguments":"{\"command\":\"cat .agents/skills/code-review/SKILL.md\"}"}}]},
	    {"role":"tool","tool_call_id":"tc1","tool_name":"terminal","content":"# skill body"},
	    {"role":"assistant","content":"done"}
	  ]
	}`
	d, err := derive.DeriveSession(agentRows("hermes", doc))
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	assertSkillTurn(t, d)
	if d.Meta.Model != "model-h" {
		t.Errorf("meta model = %q", d.Meta.Model)
	}
	if d.Meta.Title != "review my diff" {
		t.Errorf("meta title = %q", d.Meta.Title)
	}
}

// TestHermesDerive_Duration pins the session time range: messages carry no
// per-message times, so the derived bounds must span session_start →
// last_updated (not collapse to zero).
func TestHermesDerive_Duration(t *testing.T) {
	doc := `{
	  "session_id":"h-2","model":"m","session_start":"2026-06-03T10:00:00Z",
	  "last_updated":"2026-06-03T10:05:00Z",
	  "messages":[
	    {"role":"user","content":"do the thing with skills/code-review/SKILL.md"},
	    {"role":"assistant","content":"done"}
	  ]
	}`
	d, err := derive.DeriveSession(agentRows("hermes", doc))
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if got := d.Meta.EndedMs - d.Meta.StartedMs; got != 5*60*1000 {
		t.Errorf("duration = %dms, want 300000 (session_start → last_updated)", got)
	}
}
