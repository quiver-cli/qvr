package derive_test

import (
	"testing"

	"github.com/astra-sh/qvr/internal/ops/derive"
)

// These tests pin the load-path evidence for agents whose first-class skill
// tool is name-keyed (no path in the call itself): the artifact's real
// location arrives in a separate native record — the tool's result, a
// metadata field, or a follow-up event — and the deriver must harvest it so
// the invocation carries version identity, not just the skill tag.

// findSkillSpan returns the first SKILL span, or fails the test.
func findSkillSpan(t *testing.T, d *derive.Derivation) *derive.Span {
	t.Helper()
	for i := range d.Spans {
		if d.Spans[i].Kind == derive.KindSkill {
			return &d.Spans[i]
		}
	}
	t.Fatalf("no SKILL span derived: %+v", d.Spans)
	return nil
}

// TestCopilotSkillInvokedEvent: copilot's "skill" tool call is name-only; the
// skill.invoked event that follows carries the loaded SKILL.md's path.
func TestCopilotSkillInvokedEvent(t *testing.T) {
	d, err := derive.DeriveSession(agentRows("copilot",
		`{"type":"user.message","timestamp":"2026-06-03T10:00:01.000Z","data":{"content":"run code review"}}`,
		`{"type":"assistant.message","timestamp":"2026-06-03T10:00:02.000Z","data":{"content":"","toolRequests":[{"toolCallId":"tc1","name":"skill","arguments":{"skill":"code-review"}}]}}`,
		`{"type":"tool.execution_complete","timestamp":"2026-06-03T10:00:03.000Z","data":{"toolCallId":"tc1","success":true,"result":{"content":"Skill \"code-review\" loaded successfully."}}}`,
		`{"type":"skill.invoked","timestamp":"2026-06-03T10:00:03.100Z","data":{"name":"code-review","path":"/tmp/proj/.github/skills/code-review/SKILL.md","source":"project"}}`,
	))
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	sp := findSkillSpan(t, d)
	if got := sp.Attributes["skill.name"]; got != "code-review" {
		t.Errorf("skill.name = %v", got)
	}
	if got := sp.Attributes["skill.load_path"]; got != "/tmp/proj/.github/skills/code-review/SKILL.md" {
		t.Errorf("skill.load_path = %v, want the skill.invoked event path", got)
	}
}

// TestHermesSkillViewResultDir: hermes's skill_view call is name-only; its
// RESULT inlines the loaded skill's directory as "skill_dir", which the
// shared result miner attaches as the span's load path.
func TestHermesSkillViewResultDir(t *testing.T) {
	d, err := derive.DeriveSession(agentRows("hermes",
		`{"type":"session","model":"model-h"}`,
		`{"type":"message","role":"user","content":"run code review","timestamp":1781212600}`,
		`{"type":"message","role":"assistant","content":"","tool_calls":[{"id":"tc1","function":{"name":"skill_view","arguments":"{\"name\": \"code-review\"}"}}],"timestamp":1781212601}`,
		`{"type":"message","role":"tool","tool_call_id":"tc1","content":"{\"success\": true, \"name\": \"code-review\", \"skill_dir\": \"/home/u/.hermes/skills/code-review\", \"content\": \"# body\"}","timestamp":1781212602}`,
	))
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	sp := findSkillSpan(t, d)
	if got := sp.Attributes["skill.name"]; got != "code-review" {
		t.Errorf("skill.name = %v", got)
	}
	if got := sp.Attributes["skill.load_path"]; got != "/home/u/.hermes/skills/code-review" {
		t.Errorf("skill.load_path = %v, want the result's skill_dir", got)
	}
}

// TestHermesSkillViewResultDir_NameGate: a result that mentions some OTHER
// skill's directory must not be attributed to this span.
func TestHermesSkillViewResultDir_NameGate(t *testing.T) {
	d, err := derive.DeriveSession(agentRows("hermes",
		`{"type":"session","model":"model-h"}`,
		`{"type":"message","role":"user","content":"run code review","timestamp":1781212600}`,
		`{"type":"message","role":"assistant","content":"","tool_calls":[{"id":"tc1","function":{"name":"skill_view","arguments":"{\"name\": \"code-review\"}"}}],"timestamp":1781212601}`,
		`{"type":"message","role":"tool","tool_call_id":"tc1","content":"see also /home/u/.hermes/skills/other-skill for related work","timestamp":1781212602}`,
	))
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	sp := findSkillSpan(t, d)
	if got, ok := sp.Attributes["skill.load_path"]; ok {
		t.Errorf("skill.load_path = %v, want none (path belongs to a different skill)", got)
	}
}

// TestHermesSkillViewFileRead: skill_view with a file_path argument reads a
// supporting file — it stays a TOOL span tagged with the skill, never a
// second load.
func TestHermesSkillViewFileRead(t *testing.T) {
	d, err := derive.DeriveSession(agentRows("hermes",
		`{"type":"session","model":"model-h"}`,
		`{"type":"message","role":"user","content":"run code review","timestamp":1781212600}`,
		`{"type":"message","role":"assistant","content":"","tool_calls":[{"id":"tc1","function":{"name":"skill_view","arguments":"{\"name\": \"code-review\", \"file_path\": \"references/checks.md\"}"}}],"timestamp":1781212601}`,
		`{"type":"message","role":"tool","tool_call_id":"tc1","content":"{\"success\": true, \"name\": \"code-review\", \"file\": \"references/checks.md\", \"content\": \"# checks\"}","timestamp":1781212602}`,
	))
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	for i := range d.Spans {
		if d.Spans[i].Kind == derive.KindSkill {
			t.Fatalf("file read lifted to a SKILL span: %+v", d.Spans[i].Attributes)
		}
		if d.Spans[i].Kind == derive.KindTool {
			if got := d.Spans[i].Attributes["skill.name"]; got != "code-review" {
				t.Errorf("tool span skill.name = %v, want attribution to code-review", got)
			}
		}
	}
}

// TestOpencodeSkillMetadataDir: opencode's "skill" tool call is name-only;
// its state.metadata.dir records the loaded skill's directory.
func TestOpencodeSkillMetadataDir(t *testing.T) {
	d, err := derive.DeriveSession(agentRows("opencode",
		`{"type":"message","id":"mu","data":{"role":"user"},"time_created":1781215000000}`,
		`{"type":"part","message_id":"mu","data":{"type":"text","text":"run code review"},"time_created":1781215001000}`,
		`{"type":"message","id":"ma","data":{"role":"assistant","model":{"providerID":"prov","modelID":"model-o"}},"time_created":1781215002000}`,
		`{"type":"part","message_id":"ma","data":{"type":"tool","tool":"skill","callID":"c1","state":{"input":{"name":"code-review"},"metadata":{"dir":"/tmp/proj/.claude/skills/code-review"},"output":"<skill_content name=\"code-review\"># body</skill_content>"}},"time_created":1781215003000}`,
	))
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	sp := findSkillSpan(t, d)
	if got := sp.Attributes["skill.name"]; got != "code-review" {
		t.Errorf("skill.name = %v", got)
	}
	if got := sp.Attributes["skill.load_path"]; got != "/tmp/proj/.claude/skills/code-review" {
		t.Errorf("skill.load_path = %v, want state.metadata.dir", got)
	}
}

// TestOpencodeSkillOutputMined: without a metadata.dir, the skill tool's
// output (which inlines the skill's base directory) still yields the path
// via the shared result miner.
func TestOpencodeSkillOutputMined(t *testing.T) {
	d, err := derive.DeriveSession(agentRows("opencode",
		`{"type":"message","id":"mu","data":{"role":"user"},"time_created":1781215000000}`,
		`{"type":"part","message_id":"mu","data":{"type":"text","text":"run code review"},"time_created":1781215001000}`,
		`{"type":"message","id":"ma","data":{"role":"assistant"},"time_created":1781215002000}`,
		`{"type":"part","message_id":"ma","data":{"type":"tool","tool":"skill","callID":"c1","state":{"input":{"name":"code-review"},"output":"Base directory for this skill: file:///tmp/proj/.claude/skills/code-review\n# body"}},"time_created":1781215003000}`,
	))
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	sp := findSkillSpan(t, d)
	if got, _ := sp.Attributes["skill.load_path"].(string); got != "/tmp/proj/.claude/skills/code-review" {
		t.Errorf("skill.load_path = %q, want the base-directory path with the file:// scheme stripped", got)
	}
}

// TestClaudeParallelSkillBaseDirs: two Skill calls in ONE assistant message
// (parallel tool use) are followed by two base-directory injections; each
// must land on its own span — a nameless reverse search would swap them.
func TestClaudeParallelSkillBaseDirs(t *testing.T) {
	d, err := derive.DeriveSession(agentRows("claude",
		`{"type":"user","timestamp":"2026-06-03T10:00:01.000Z","message":{"role":"user","content":"run both skills"}}`,
		`{"type":"assistant","timestamp":"2026-06-03T10:00:02.000Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Skill","input":{"skill":"code-review"}},{"type":"tool_use","id":"t2","name":"Skill","input":{"skill":"qa-demo"}}]}}`,
		`{"type":"user","timestamp":"2026-06-03T10:00:03.000Z","isMeta":true,"message":{"role":"user","content":[{"type":"text","text":"Base directory for this skill: /p/.claude/skills/code-review\n# body A"}]}}`,
		`{"type":"user","timestamp":"2026-06-03T10:00:04.000Z","isMeta":true,"message":{"role":"user","content":[{"type":"text","text":"Base directory for this skill: /p/.claude/skills/qa-demo\n# body B"}]}}`,
	))
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	want := map[string]string{
		"code-review": "/p/.claude/skills/code-review",
		"qa-demo":     "/p/.claude/skills/qa-demo",
	}
	for i := range d.Spans {
		sp := &d.Spans[i]
		if sp.Kind != derive.KindSkill {
			continue
		}
		name, _ := sp.Attributes["skill.name"].(string)
		if got := sp.Attributes["skill.load_path"]; got != want[name] {
			t.Errorf("%s load_path = %v, want %s", name, got, want[name])
		}
		delete(want, name)
	}
	if len(want) > 0 {
		t.Errorf("missing SKILL spans for %v", want)
	}
}
