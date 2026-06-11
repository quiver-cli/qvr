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
//	{"type":"session.model_change","data":{"newModel":"…"}}
//
// Skill attribution is the shared path signal over each tool request's
// command/arguments.
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
	}
	w.flush()
	out.Spans = w.spans
	return out, nil
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
	}
}
