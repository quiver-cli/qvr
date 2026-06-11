package derive_test

import (
	"strings"
	"testing"
	"time"

	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/google/uuid"
)

// codexRow builds a transcript RawTrace from a verbatim rollout JSONL line.
func codexRow(sid uuid.UUID, seq int, raw string) *ops.RawTrace {
	return &ops.RawTrace{
		AgentName:  "codex",
		SessionID:  sid,
		Source:     ops.RawSourceTranscript,
		Seq:        seq,
		CapturedAt: time.Now(),
		Raw:        []byte(raw),
	}
}

// TestCodexDerive_TurnToolSkill mirrors the shape of a real Codex rollout: a
// task_started opens the turn, turn_context names the model, an injected
// <skills_instructions> developer message lists the available skills, the
// user_message event carries the prompt, two function_call/function_call_output
// pairs run tools, two token_count events accumulate usage, and an assistant
// message plus task_complete close the turn. One tool reads the skill's
// SKILL.md by path (Codex's own skill mechanism), which must lift to a SKILL
// span — without any dependency on `qvr`.
func TestCodexDerive_TurnToolSkill(t *testing.T) {
	sid := uuid.MustParse("019e88f6-6dca-7c63-89d1-74e9c5f2eac9")
	rows := []*ops.RawTrace{
		codexRow(sid, 0, `{"timestamp":"2026-06-02T15:31:51.964Z","type":"session_meta","payload":{"id":"019e88f6","cwd":"/x","model_provider":"openai"}}`),
		codexRow(sid, 1, `{"timestamp":"2026-06-02T15:31:51.966Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`),
		// Injected context: the prompt must NOT be mined from here, but the
		// <skills_instructions> registry of available skills must be.
		codexRow(sid, 2, `{"timestamp":"2026-06-02T15:31:53.950Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>"},{"type":"input_text","text":"<skills_instructions>\n### Available skills\n- code-review: Review pending changes. (file: /Users/x/.quiver/worktrees/raks/code-review/94e539b/skills/code-review/SKILL.md)\n</skills_instructions>"}]}}`),
		codexRow(sid, 3, `{"timestamp":"2026-06-02T15:31:53.950Z","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-5.5"}}`),
		codexRow(sid, 4, `{"timestamp":"2026-06-02T15:31:54.008Z","type":"event_msg","payload":{"type":"user_message","message":"list files then read the review skill"}}`),
		// Codex's native skill load: the model opens the skill's SKILL.md by
		// path → SKILL span.
		codexRow(sid, 5, `{"timestamp":"2026-06-02T15:32:03.518Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"sed -n '1,40p' .codex/skills/code-review/SKILL.md\"}","call_id":"call_skill"}}`),
		// An ordinary tool call → TOOL span.
		codexRow(sid, 6, `{"timestamp":"2026-06-02T15:32:03.521Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"ls\"}","call_id":"call_ls"}}`),
		codexRow(sid, 7, `{"timestamp":"2026-06-02T15:32:03.618Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_ls","output":"AGENTS.md\nCLAUDE.md"}}`),
		codexRow(sid, 8, `{"timestamp":"2026-06-02T15:32:03.618Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":23896,"output_tokens":113}}}}`),
		codexRow(sid, 9, `{"timestamp":"2026-06-02T15:32:06.325Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}}`),
		codexRow(sid, 10, `{"timestamp":"2026-06-02T15:32:06.386Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":24160,"output_tokens":85}}}}`),
		codexRow(sid, 11, `{"timestamp":"2026-06-02T15:32:06.415Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","last_agent_message":"done"}}`),
	}

	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	spans := d.Spans

	llm, tool, skill := splitSpanKinds(spans)
	if llm == nil || tool == nil || skill == nil {
		t.Fatalf("want LLM+TOOL+SKILL spans, got %d spans (%+v)", len(spans), spans)
	}

	// LLM turn: model from turn_context, tokens summed across both token_count
	// events, prompt from the user_message event (not the injected context).
	wantAttrs(t, llm, map[string]any{
		"gen_ai.request.model":       "gpt-5.5",
		"gen_ai.provider.name":       "openai",
		"gen_ai.usage.input_tokens":  23896 + 24160,
		"gen_ai.usage.output_tokens": 113 + 85,
	})
	if s, _ := llm.Attributes["gen_ai.input.messages"].(string); !strings.Contains(s, "list files then read the review skill") {
		t.Errorf("input messages missing prompt: got %v", llm.Attributes["gen_ai.input.messages"])
	}
	if s, _ := llm.Attributes["gen_ai.input.messages"].(string); strings.Contains(s, "permissions instructions") {
		t.Errorf("prompt leaked injected developer context: %v", s)
	}
	if s, _ := llm.Attributes["gen_ai.output.messages"].(string); !strings.Contains(s, "done") {
		t.Errorf("output messages missing text: got %v", llm.Attributes["gen_ai.output.messages"])
	}

	// SKILL span: a `sed -n '1,40p' .codex/skills/code-review/SKILL.md` exec —
	// Codex reading its native SKILL.md by path — lifted to a skill load.
	if skill.Attributes["skill.name"] != "code-review" {
		t.Errorf("skill.name: got %v", skill.Attributes["skill.name"])
	}
	if skill.ParentSpanID != llm.SpanID {
		t.Errorf("skill should parent to the turn LLM span")
	}

	// TOOL span: result attached from function_call_output, parents to the turn.
	wantAttrs(t, tool, map[string]any{
		"gen_ai.tool.name":        "exec_command",
		"gen_ai.tool.call.result": "AGENTS.md\nCLAUDE.md",
		"gen_ai.tool.description": "ls",
	})
	if tool.ParentSpanID != llm.SpanID {
		t.Errorf("tool should parent to the turn LLM span")
	}

	// Determinism: re-derivation reproduces identical ids.
	again, _ := derive.DeriveSession(rows)
	if again.Spans[0].SpanID != spans[0].SpanID || again.Spans[0].TraceID != spans[0].TraceID {
		t.Error("derivation is not deterministic")
	}

	// #147: a command's begin (function_call) and end (function_call_output)
	// must collapse onto ONE span, so every emitted span_id is unique. A dup
	// here is exactly what tripped the UNIQUE constraint and dropped sessions.
	assertUniqueSpanIDs(t, spans)

	// Unified session meta: model, prompt title, counts, and the skill list.
	wantMeta(t, d.Meta, "codex", "list files then read the review skill", "gpt-5.5", 1, 1, "code-review")
}

// splitSpanKinds returns the last LLM, TOOL, and SKILL span (each may be nil).
func splitSpanKinds(spans []derive.Span) (llm, tool, skill *derive.Span) {
	for i := range spans {
		switch spans[i].Kind {
		case derive.KindLLM:
			llm = &spans[i]
		case derive.KindTool:
			tool = &spans[i]
		case derive.KindSkill:
			skill = &spans[i]
		}
	}
	return llm, tool, skill
}

// wantAttrs asserts each key on sp.Attributes equals the expected value.
func wantAttrs(t *testing.T, sp *derive.Span, want map[string]any) {
	t.Helper()
	for k, v := range want {
		if got := sp.Attributes[k]; got != v {
			t.Errorf("%s: want %v, got %v", k, v, got)
		}
	}
}

// assertUniqueSpanIDs fails if any span_id repeats — i.e. a command's begin+end
// did not collapse onto one span.
func assertUniqueSpanIDs(t *testing.T, spans []derive.Span) {
	t.Helper()
	ids := map[string]bool{}
	for _, sp := range spans {
		if ids[sp.SpanID] {
			t.Errorf("duplicate span_id %q — begin+end did not collapse", sp.SpanID)
		}
		ids[sp.SpanID] = true
	}
}

// TestCodexDerive_Title confirms the shared title logic works for codex.
func TestCodexDerive_Title(t *testing.T) {
	sid := uuid.New()
	rows := []*ops.RawTrace{
		codexRow(sid, 0, `{"timestamp":"2026-06-02T15:31:51.966Z","type":"event_msg","payload":{"type":"task_started"}}`),
		codexRow(sid, 1, `{"timestamp":"2026-06-02T15:31:54.008Z","type":"event_msg","payload":{"type":"user_message","message":"fix the codex deriver"}}`),
	}
	if got := metaTitle(t, rows); got != "fix the codex deriver" {
		t.Errorf("title: got %q", got)
	}
}
