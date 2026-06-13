package derive

import (
	"encoding/json"

	"github.com/astra-sh/qvr/internal/ops"
)

func init() { Register("copilot", copilotDeriver{}) }

// copilotDeriver reconstructs the Turn→Tool/Skill hierarchy from a GitHub
// Copilot CLI session-state transcript (JSONL under ~/.copilot/session-state;
// see https://docs.github.com/en/copilot/concepts/agents/about-copilot-cli).
// Each line is a typed event envelope:
//
//	{"type":"session.start","timestamp",…,"data":{"sessionId":"…"}}
//	{"type":"user.message","data":{"content":"…"}}
//	{"type":"assistant.message","data":{"content":"…","toolRequests":[
//	    {"toolCallId","name","arguments"}]}}
//	{"type":"tool.execution_complete","data":{"toolCallId","success",
//	    "result":{"content":"…"}}}
//	{"type":"skill.invoked","data":{"name":"…","path":"…/SKILL.md"}}
//	{"type":"session.model_change","data":{"newModel":"…"}}
//
// Skill attribution: copilot's first-class "skill" tool is name-only
// (resolved by ops.SkillRefFromTool), and the skill.invoked event that
// follows it carries the loaded SKILL.md's path — the load-path evidence
// (observed live, 2026-06-11). The shared path signal over each tool
// request's command/arguments covers supporting-file reads.
type copilotDeriver struct{}

// copilotLine is the event envelope.
type copilotLine struct {
	Type      string          `json:"type"`
	Timestamp json.RawMessage `json:"timestamp"`
	Data      copilotData     `json:"data"`
}

// copilotData is the union of the per-type data fields we read.
type copilotData struct {
	Content      string               `json:"content"`
	ToolRequests []copilotToolRequest `json:"toolRequests"`
	ToolCallID   string               `json:"toolCallId"`
	Success      *bool                `json:"success"`
	Result       *copilotToolResult   `json:"result"`
	NewModel     string               `json:"newModel"`
	Name         string               `json:"name"` // skill.invoked: skill name
	Path         string               `json:"path"` // skill.invoked: loaded SKILL.md path

	// assistant.message: per-message output tokens. Pointer: absence ≠ 0.
	// The input side is never reported per message — only at shutdown.
	OutputTokens *int `json:"outputTokens"`
	// session.shutdown: per-model session usage rollups (observed live,
	// 2026-06-12: usage.{inputTokens,outputTokens,cacheReadTokens,
	// cacheWriteTokens,reasoningTokens}; inputTokens includes the cache
	// reads/writes). The session-level token totals come from here.
	ModelMetrics map[string]copilotModelMetrics `json:"modelMetrics"`
}

type copilotModelMetrics struct {
	Usage *copilotUsage `json:"usage"`
}

type copilotUsage struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
}

type copilotToolRequest struct {
	ToolCallID string         `json:"toolCallId"`
	Name       string         `json:"name"`
	Arguments  map[string]any `json:"arguments"`
}

type copilotToolResult struct {
	Content string `json:"content"`
}

func (copilotDeriver) Derive(rows []*ops.RawTrace) (*Derivation, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	w := &turnWalk{sessionID: rows[0].SessionID.String()}
	out := &Derivation{}

	for _, r := range rows {
		if r.Source != ops.RawSourceTranscript {
			continue
		}
		var ln copilotLine
		if err := json.Unmarshal(r.Raw, &ln); err != nil {
			continue
		}
		copilotEvent(w, &ln, flexTimeMs(ln.Timestamp))
		copilotShutdownUsage(&out.Meta, &ln)
	}
	w.flush()
	out.Spans = w.spans
	return out, nil
}

// copilotShutdownUsage folds a session.shutdown event's per-model usage
// rollups into the session token totals. Copilot reports input tokens only
// here (per message it reports just outputTokens), so this is the one source
// for the session's input side.
func copilotShutdownUsage(m *SessionMeta, ln *copilotLine) {
	if normType(ln.Type) != "sessionshutdown" || len(ln.Data.ModelMetrics) == 0 {
		return
	}
	var in, out int64
	seen := false
	for _, mm := range ln.Data.ModelMetrics {
		if mm.Usage == nil {
			continue
		}
		seen = true
		in += mm.Usage.InputTokens
		out += mm.Usage.OutputTokens
	}
	if seen {
		// Last write wins by design: modelMetrics reports cumulative
		// session-level totals, so if a session emits more than one shutdown
		// (e.g. after a model switch or reconnect) the final one carries the most
		// complete totals. We intentionally overwrite rather than accumulate.
		m.TokensIn = &in
		m.TokensOut = &out
	}
}

// copilotEvent folds one event envelope into the walk.
func copilotEvent(w *turnWalk, ln *copilotLine, ts int64) {
	switch normType(ln.Type) {
	case "sessionmodelchange":
		w.setModel(ln.Data.NewModel)
	case "usermessage":
		prompt := stripSystemReminder(ln.Data.Content)
		if prompt == "" {
			return
		}
		w.open(ts)
		w.cur.prompt = prompt
	case "assistantmessage":
		w.ensure(ts)
		if ln.Data.OutputTokens != nil {
			w.cur.addOutputUsage(*ln.Data.OutputTokens)
		}
		if ln.Data.Content != "" {
			w.cur.appendOutput(ln.Data.Content)
		}
		for _, tr := range ln.Data.ToolRequests {
			args := compactJSON(tr.Arguments)
			cmd := commandFromArgs(tr.Arguments)
			ref := resolveSkillRef(tr.Name, tr.Arguments, cmd, args, nil)
			w.cur.addToolInvocation(tr.Name, tr.ToolCallID, args, cmd, ref, ts, w.sessionID)
		}
		w.cur.bump(ts)
	case "toolexecutioncomplete":
		if w.cur == nil || ln.Data.ToolCallID == "" {
			return
		}
		result := ""
		if ln.Data.Result != nil {
			result = ln.Data.Result.Content
		}
		failed := ln.Data.Success != nil && !*ln.Data.Success
		w.cur.applyResult(ln.Data.ToolCallID, result, ts, failed)
	case "skillinvoked":
		// The path evidence for the turn's pending skill-tool SKILL span,
		// emitted right after the tool call it belongs to.
		if w.cur != nil {
			w.cur.attachSkillLoadPath(ln.Data.Name, ln.Data.Path)
		}
	}
}
