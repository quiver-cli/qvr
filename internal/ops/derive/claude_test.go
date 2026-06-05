package derive_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/quiver-cli/qvr/internal/ops"
	"github.com/quiver-cli/qvr/internal/ops/derive"
)

// row builds a transcript RawTrace from a verbatim JSONL line.
func row(sid uuid.UUID, seq int, raw string) *ops.RawTrace {
	return &ops.RawTrace{
		AgentName:  "claude-code",
		SessionID:  sid,
		Source:     ops.RawSourceTranscript,
		Seq:        seq,
		CapturedAt: time.Now(),
		Raw:        []byte(raw),
	}
}

func TestClaudeDerive_TurnToolSkill(t *testing.T) {
	sid := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	rows := []*ops.RawTrace{
		row(sid, 0, `{"type":"user","timestamp":"2026-06-02T00:00:00.000Z","message":{"role":"user","content":"add a feature"}}`),
		// assistant: thinking + a Skill load + a Read tool call, with usage+model.
		row(sid, 1, `{"type":"assistant","timestamp":"2026-06-02T00:00:01.000Z","message":{"role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":100,"output_tokens":20},"content":[`+
			`{"type":"thinking","thinking":"plan"},`+
			`{"type":"tool_use","id":"toolu_skill","name":"Skill","input":{"command":"code-review"}},`+
			`{"type":"tool_use","id":"toolu_read","name":"Read","input":{"file_path":"/x/main.go"}}]}}`),
		// tool_result for the Read.
		row(sid, 2, `{"type":"user","timestamp":"2026-06-02T00:00:02.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_read","content":"package main","is_error":false}]}}`),
		// final assistant text.
		row(sid, 3, `{"type":"assistant","timestamp":"2026-06-02T00:00:03.000Z","message":{"role":"assistant","model":"claude-opus-4-8","usage":{"output_tokens":5},"content":[{"type":"text","text":"done"}]}}`),
	}

	spans, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}

	var llm, tool, skill *derive.Span
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
	if llm == nil || tool == nil || skill == nil {
		t.Fatalf("want LLM+TOOL+SKILL spans, got %d spans (%+v)", len(spans), spans)
	}

	// LLM turn: OTel gen_ai chat conventions — tokens, model, messages.
	if got := llm.Attributes["gen_ai.usage.input_tokens"]; got != 100 {
		t.Errorf("input tokens: want 100, got %v", got)
	}
	if got := llm.Attributes["gen_ai.usage.output_tokens"]; got != 25 {
		t.Errorf("output tokens: want 25, got %v", got)
	}
	if llm.Attributes["gen_ai.request.model"] != "claude-opus-4-8" {
		t.Errorf("model: got %v", llm.Attributes["gen_ai.request.model"])
	}
	if llm.Attributes["gen_ai.provider.name"] != "anthropic" {
		t.Errorf("provider: got %v", llm.Attributes["gen_ai.provider.name"])
	}
	if s, _ := llm.Attributes["gen_ai.input.messages"].(string); !strings.Contains(s, "add a feature") {
		t.Errorf("input messages missing prompt: got %v", llm.Attributes["gen_ai.input.messages"])
	}
	if s, _ := llm.Attributes["gen_ai.output.messages"].(string); !strings.Contains(s, "done") {
		t.Errorf("output messages missing text: got %v", llm.Attributes["gen_ai.output.messages"])
	}

	// SKILL span: OTel execute_tool + the Quiver skill.name extension tag.
	if skill.Attributes["skill.name"] != "code-review" {
		t.Errorf("skill.name: got %v", skill.Attributes["skill.name"])
	}
	if skill.Attributes["gen_ai.operation.name"] != "execute_tool" {
		t.Errorf("skill operation: got %v", skill.Attributes["gen_ai.operation.name"])
	}
	if skill.ParentSpanID != llm.SpanID {
		t.Errorf("skill should parent to the turn LLM span")
	}

	// TOOL span: gen_ai.tool.* with result attached, parents to the turn.
	if tool.Attributes["gen_ai.tool.name"] != "Read" {
		t.Errorf("tool name: got %v", tool.Attributes["gen_ai.tool.name"])
	}
	if tool.Attributes["gen_ai.tool.call.result"] != "package main" {
		t.Errorf("tool result: got %v", tool.Attributes["gen_ai.tool.call.result"])
	}
	if tool.ParentSpanID != llm.SpanID {
		t.Errorf("tool should parent to the turn LLM span")
	}

	// Determinism: re-derivation reproduces identical ids.
	again, _ := derive.DeriveSession(rows)
	if again[0].SpanID != spans[0].SpanID || again[0].TraceID != spans[0].TraceID {
		t.Error("derivation is not deterministic")
	}
}
