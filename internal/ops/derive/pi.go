package derive

import (
	"encoding/json"

	"github.com/astra-sh/qvr/internal/ops"
)

func init() { Register("pi", piDeriver{}) }

// piDeriver reconstructs the Turn→Tool/Skill hierarchy from a Pi coding-agent
// session transcript (JSONL; format per
// https://github.com/badlogic/pi-mono/tree/main/packages/coding-agent):
//
//	{"type":"session","id","timestamp","cwd","parentSession"?}
//	{"type":"message","id","parentId","timestamp","message":{…}}
//	{"type":"model_change","modelId":"…"}
//
// Message roles: "user" (content string or text blocks) opens a turn;
// "assistant" carries text / thinking / toolCall blocks plus an optional
// model; "toolResult" closes a pending tool via toolCallId; "bashExecution"
// is a native shell record (command/output/exitCode) emitted as a completed
// TOOL span. Sessions can branch via parentId; the walk is file-order, which
// matches the append order of the active branch.
type piDeriver struct{}

// piLine is the typed record envelope.
type piLine struct {
	Type      string          `json:"type"`
	Timestamp json.RawMessage `json:"timestamp"`
	ModelID   string          `json:"modelId"`
	Message   json.RawMessage `json:"message"`
}

// piMessage is the message body, a superset of the block-message core with
// Pi's native bash-execution fields.
type piMessage struct {
	Role       string          `json:"role"`
	Model      string          `json:"model"`
	ToolCallID string          `json:"toolCallId"`
	Content    json.RawMessage `json:"content"`
	Command    string          `json:"command"`
	Output     string          `json:"output"`
	ExitCode   int             `json:"exitCode"`
	IsError    bool            `json:"isError"`
}

func (piDeriver) Derive(rows []*ops.RawTrace) (*Derivation, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	w := &turnWalk{sessionID: rows[0].SessionID.String()}
	out := &Derivation{}

	for _, r := range rows {
		if r.Source != ops.RawSourceTranscript {
			continue
		}
		var ln piLine
		if err := json.Unmarshal(r.Raw, &ln); err != nil {
			continue
		}
		ts := flexTimeMs(ln.Timestamp)

		switch normType(ln.Type) {
		case "modelchange":
			w.setModel(ln.ModelID)
		case "message":
			var msg piMessage
			if err := json.Unmarshal(ln.Message, &msg); err != nil {
				continue
			}
			if normType(msg.Role) == "bashexecution" {
				// Native shell record: a complete tool invocation in one line.
				w.ensure(ts)
				w.cur.addCommandTool("bash", "", "", msg.Command, ts, w.sessionID, nil)
				w.cur.applyResultLast(msg.Output, ts, msg.IsError || msg.ExitCode != 0)
				w.cur.bump(ts)
				continue
			}
			handleBlockMessage(w, blockMsg{
				role:       msg.Role,
				model:      msg.Model,
				toolCallID: msg.ToolCallID,
				text:       openclawText(msg.Content),
				blocks:     decodeOpenclawBlocks(msg.Content),
			}, ts)
		}
	}
	w.flush()
	out.Spans = w.spans
	return out, nil
}

// applyResultLast attaches a result to the most recently added tool span —
// for formats whose native records carry call and result in one line (no id).
func (t *turn) applyResultLast(result string, ts int64, isError bool) {
	if len(t.tools) == 0 {
		return
	}
	sp := &t.tools[len(t.tools)-1]
	sp.Attributes["gen_ai.tool.call.result"] = result
	if isError {
		sp.Attributes["error.type"] = "tool_failure"
	}
	if ts > sp.StartMs {
		sp.EndMs = ts
	}
}
