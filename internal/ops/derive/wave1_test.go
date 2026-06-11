package derive_test

import (
	"testing"
	"time"

	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/google/uuid"
)

// agentRows builds transcript RawTraces for any agent from verbatim lines.
func agentRows(agent string, lines ...string) []*ops.RawTrace {
	sid := uuid.NewSHA1(uuid.NameSpaceOID, []byte(agent+"-fixture"))
	rows := make([]*ops.RawTrace, 0, len(lines))
	for i, l := range lines {
		rows = append(rows, &ops.RawTrace{
			AgentName:  agent,
			SessionID:  sid,
			Source:     ops.RawSourceTranscript,
			Seq:        i,
			CapturedAt: time.Now(),
			Raw:        []byte(l),
		})
	}
	return rows
}

// TestWave1Derivers exercises each JSONL deriver over a fixture in the
// agent's real record shape: one turn whose tool invocation reads a skill's
// SKILL.md (the shared path signal), with the result attached. Each case
// asserts the span hierarchy, the skill attribution, and the unified meta.
func TestWave1Derivers(t *testing.T) {
	cases := []struct {
		agent string
		lines []string
		model string
		tools int64
	}{
		{
			agent: "openclaw",
			lines: []string{
				`{"type":"session","version":3,"id":"oc-1","timestamp":"2026-06-03T10:00:00.000Z","cwd":"/tmp/proj"}`,
				`{"type":"modelchange","modelId":"model-x","provider":"prov"}`,
				`{"type":"message","id":"m1","timestamp":"2026-06-03T10:00:01.000Z","message":{"role":"user","content":[{"type":"text","text":"review my diff"}]}}`,
				`{"type":"message","id":"m2","parentId":"m1","timestamp":"2026-06-03T10:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"loading"},{"type":"toolCall","id":"tc1","name":"exec","arguments":{"command":"cat .agents/skills/code-review/SKILL.md"}}]}}`,
				`{"type":"message","id":"m3","parentId":"m2","timestamp":"2026-06-03T10:00:03.000Z","message":{"role":"toolResult","toolCallId":"tc1","toolName":"exec","content":[{"type":"text","text":"# skill body"}]}}`,
				`{"type":"message","id":"m4","parentId":"m3","timestamp":"2026-06-03T10:00:04.000Z","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`,
			},
			model: "model-x",
			tools: 0, // the single invocation lifts to a SKILL span
		},
		{
			agent: "pi",
			lines: []string{
				`{"type":"session","id":"pi-1","timestamp":"2026-06-03T10:00:00.000Z","cwd":"/tmp/proj"}`,
				`{"type":"model_change","modelId":"model-y"}`,
				`{"type":"message","id":"p1","timestamp":"2026-06-03T10:00:01.000Z","message":{"role":"user","content":"review my diff"}}`,
				`{"type":"message","id":"p2","parentId":"p1","timestamp":"2026-06-03T10:00:02.000Z","message":{"role":"assistant","model":"model-y","content":[{"type":"thinking","thinking":"plan"},{"type":"toolCall","id":"tc1","name":"read","arguments":{"path":".pi/skills/code-review/SKILL.md"}}]}}`,
				`{"type":"message","id":"p3","parentId":"p2","timestamp":"2026-06-03T10:00:03.000Z","message":{"role":"toolResult","toolCallId":"tc1","toolName":"read","content":[{"type":"text","text":"# skill body"}]}}`,
				`{"type":"message","id":"p4","parentId":"p3","timestamp":"2026-06-03T10:00:04.000Z","message":{"role":"bashExecution","command":"go test ./...","output":"ok","exitCode":0}}`,
				`{"type":"message","id":"p5","parentId":"p4","timestamp":"2026-06-03T10:00:05.000Z","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`,
			},
			model: "model-y",
			tools: 1, // the bash execution; the read lifts to SKILL
		},
		{
			agent: "copilot",
			lines: []string{
				`{"type":"session.start","timestamp":"2026-06-03T10:00:00.000Z","data":{"sessionId":"cp-1"}}`,
				`{"type":"session.model_change","timestamp":"2026-06-03T10:00:00.500Z","data":{"newModel":"model-z"}}`,
				`{"type":"user.message","timestamp":"2026-06-03T10:00:01.000Z","data":{"content":"review my diff"}}`,
				`{"type":"assistant.message","timestamp":"2026-06-03T10:00:02.000Z","data":{"content":"loading","toolRequests":[{"toolCallId":"tc1","name":"bash","arguments":{"command":"cat .github/skills/code-review/SKILL.md"}}]}}`,
				`{"type":"tool.execution_complete","timestamp":"2026-06-03T10:00:03.000Z","data":{"toolCallId":"tc1","success":true,"result":{"content":"# skill body"}}}`,
				`{"type":"assistant.message","timestamp":"2026-06-03T10:00:04.000Z","data":{"content":"done"}}`,
			},
			model: "model-z",
			tools: 0,
		},
		{
			agent: "droid",
			lines: []string{
				`{"type":"session_start","id":"dr-1","cwd":"/tmp/proj","timestamp":"2026-06-03T10:00:00.000Z"}`,
				`{"type":"message","id":"d1","timestamp":"2026-06-03T10:00:01.000Z","message":{"role":"user","content":[{"type":"text","text":"review my diff"}]}}`,
				`{"type":"message","id":"d2","timestamp":"2026-06-03T10:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"loading"},{"type":"tool_use","id":"tc1","name":"Execute","input":{"command":"cat .factory/skills/code-review/SKILL.md"}}]}}`,
				`{"type":"message","id":"d3","timestamp":"2026-06-03T10:00:03.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tc1","content":"# skill body"}]}}`,
				`{"type":"message","id":"d4","timestamp":"2026-06-03T10:00:04.000Z","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`,
			},
			model: "", // session-store dialect has no in-file model
			tools: 0,
		},
		{
			agent: "droid", // exec-stream dialect
			lines: []string{
				`{"type":"system","session_id":"dr-2","cwd":"/tmp/proj","model":"model-d","timestamp":"2026-06-03T10:00:00.000Z"}`,
				`{"type":"message","id":"e1","role":"user","timestamp":"2026-06-03T10:00:01.000Z","text":"review my diff"}`,
				`{"type":"toolcall","toolCallId":"tc1","toolName":"Execute","parameters":{"command":"cat .factory/skills/code-review/SKILL.md"},"timestamp":"2026-06-03T10:00:02.000Z"}`,
				`{"type":"toolresult","toolCallId":"tc1","toolName":"Execute","value":"# skill body","timestamp":"2026-06-03T10:00:03.000Z"}`,
				`{"type":"completion","finalText":"done","timestamp":"2026-06-03T10:00:04.000Z"}`,
			},
			model: "model-d",
			tools: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.agent+"/"+tc.model, func(t *testing.T) {
			d, err := derive.DeriveSession(agentRows(tc.agent, tc.lines...))
			if err != nil {
				t.Fatalf("derive: %v", err)
			}
			assertSkillTurn(t, d)
			assertWave1Meta(t, d.Meta, tc.agent, tc.model, tc.tools)

			// Determinism: same rows ⇒ same ids.
			again, _ := derive.DeriveSession(agentRows(tc.agent, tc.lines...))
			if again.Spans[0].SpanID != d.Spans[0].SpanID {
				t.Error("derivation is not deterministic")
			}
			assertUniqueSpanIDs(t, d.Spans)
		})
	}
}

// assertSkillTurn checks the span hierarchy: a SKILL span attributed to
// code-review with its load path and result, parented to the turn's LLM span.
func assertSkillTurn(t *testing.T, d *derive.Derivation) {
	t.Helper()
	llm, _, skill := splitSpanKinds(d.Spans)
	if llm == nil || skill == nil {
		t.Fatalf("want LLM+SKILL spans, got %+v", d.Spans)
	}
	if skill.Attributes["skill.name"] != "code-review" {
		t.Errorf("skill.name = %v", skill.Attributes["skill.name"])
	}
	if lp, _ := skill.Attributes["skill.load_path"].(string); lp == "" {
		t.Errorf("skill.load_path missing — identity verification needs the loaded path")
	}
	if skill.ParentSpanID != llm.SpanID {
		t.Errorf("skill should parent to the turn LLM span")
	}
	if res, _ := skill.Attributes["gen_ai.tool.call.result"].(string); res != "# skill body" {
		t.Errorf("tool result not attached: %v", res)
	}
}

// assertWave1Meta checks the unified session meta fields shared by the cases.
func assertWave1Meta(t *testing.T, m derive.SessionMeta, agent, model string, tools int64) {
	t.Helper()
	if m.Agent != agent {
		t.Errorf("meta agent = %q, want %q", m.Agent, agent)
	}
	if m.Title != "review my diff" {
		t.Errorf("meta title = %q", m.Title)
	}
	if m.Model != model {
		t.Errorf("meta model = %q, want %q", m.Model, model)
	}
	if m.Tools != tools {
		t.Errorf("meta tools = %d, want %d", m.Tools, tools)
	}
	if len(m.Skills) != 1 || m.Skills[0] != "code-review" {
		t.Errorf("meta skills = %v", m.Skills)
	}
}

// TestWave1Derivers_SkilllessSessionHasNoSkillSpans pins the gate input: a
// session that touches no skill path derives only LLM/TOOL spans, so the
// discovery scan can prove it skill-less.
func TestWave1Derivers_SkilllessSessionHasNoSkillSpans(t *testing.T) {
	cases := map[string][]string{
		"openclaw": {
			`{"type":"message","id":"m1","timestamp":"2026-06-03T10:00:01.000Z","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`,
			`{"type":"message","id":"m2","timestamp":"2026-06-03T10:00:02.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"t1","name":"exec","arguments":{"command":"ls"}},{"type":"text","text":"done"}]}}`,
		},
		"pi": {
			`{"type":"message","id":"p1","timestamp":"2026-06-03T10:00:01.000Z","message":{"role":"user","content":"hi"}}`,
			`{"type":"message","id":"p2","timestamp":"2026-06-03T10:00:02.000Z","message":{"role":"bashExecution","command":"ls","output":"x","exitCode":0}}`,
		},
		"copilot": {
			`{"type":"user.message","timestamp":"2026-06-03T10:00:01.000Z","data":{"content":"hi"}}`,
			`{"type":"assistant.message","timestamp":"2026-06-03T10:00:02.000Z","data":{"content":"done"}}`,
		},
		"droid": {
			`{"type":"system","session_id":"dr","cwd":"/x","model":"m","timestamp":"2026-06-03T10:00:00.000Z"}`,
			`{"type":"message","id":"e1","role":"user","timestamp":"2026-06-03T10:00:01.000Z","text":"hi"}`,
			`{"type":"completion","finalText":"done","timestamp":"2026-06-03T10:00:02.000Z"}`,
		},
	}
	for agent, lines := range cases {
		t.Run(agent, func(t *testing.T) {
			d, err := derive.DeriveSession(agentRows(agent, lines...))
			if err != nil {
				t.Fatalf("derive: %v", err)
			}
			for _, sp := range d.Spans {
				if name, _ := sp.Attributes["skill.name"].(string); name != "" {
					t.Errorf("unexpected skill attribution: %+v", sp.Attributes)
				}
			}
			if len(d.Meta.Skills) != 0 {
				t.Errorf("meta skills = %v, want none", d.Meta.Skills)
			}
		})
	}
}
