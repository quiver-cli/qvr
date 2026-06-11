package derive

import (
	"encoding/json"

	"github.com/astra-sh/qvr/internal/ops"
)

func init() { Register("droid", droidDeriver{}) }

// droidDeriver reconstructs the Turn→Tool/Skill hierarchy from a Factory
// Droid session transcript (JSONL under ~/.factory/sessions; see
// https://docs.factory.ai/cli/getting-started). Droid writes two dialects;
// both are handled by per-line dispatch, so mixed files stay safe:
//
// Interactive session store:
//
//	{"type":"session_start","id","cwd","timestamp"}
//	{"type":"message","timestamp","message":{"role","content":[
//	    {"type":"text"|"tool_use"|"tool_result", …}]}}   // claude-style parts
//
// Exec stream:
//
//	{"type":"system","session_id","cwd","model","timestamp"}
//	{"type":"message","role","text","subtype"?}
//	{"type":"toolcall","toolCallId","toolName","parameters"}
//	{"type":"toolresult","toolCallId","value","isError"}
//	{"type":"completion","finalText"}
//
// Skill attribution is the shared path signal over each tool invocation.
type droidDeriver struct{}

// droidLine is the union envelope across both dialects.
type droidLine struct {
	Type      string          `json:"type"`
	Timestamp json.RawMessage `json:"timestamp"`

	// session store dialect
	Message json.RawMessage `json:"message"` // object → nested message

	// exec stream dialect
	Role       string          `json:"role"`
	Text       string          `json:"text"`
	Subtype    string          `json:"subtype"`
	Model      string          `json:"model"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Parameters map[string]any  `json:"parameters"`
	Value      json.RawMessage `json:"value"`
	IsError    bool            `json:"isError"`
	FinalText  string          `json:"finalText"`
}

// droidMessage is the nested message body of the session-store dialect.
type droidMessage struct {
	Role    string       `json:"role"`
	Content []droidBlock `json:"content"`
}

// droidBlock is one content part (claude-style).
type droidBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     map[string]any  `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

func (droidDeriver) Derive(rows []*ops.RawTrace) (*Derivation, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	w := &turnWalk{sessionID: rows[0].SessionID.String()}
	out := &Derivation{}

	for _, r := range rows {
		if r.Source != ops.RawSourceTranscript {
			continue
		}
		var ln droidLine
		if err := json.Unmarshal(r.Raw, &ln); err != nil {
			continue
		}
		ts := flexTimeMs(ln.Timestamp)

		switch normType(ln.Type) {
		case "system":
			w.setModel(ln.Model)
		case "message":
			droidMessageLine(w, &ln, ts)
		case "toolcall":
			w.ensure(ts)
			args := compactJSON(ln.Parameters)
			w.cur.addCommandTool(ln.ToolName, ln.ToolCallID, args, commandFromArgs(ln.Parameters), ts, w.sessionID, nil)
			w.cur.bump(ts)
		case "toolresult":
			if w.cur != nil && ln.ToolCallID != "" {
				w.cur.applyResult(ln.ToolCallID, droidValueText(ln.Value), ts, ln.IsError)
			}
		case "completion":
			w.ensure(ts)
			if w.cur.output == "" && ln.FinalText != "" {
				w.cur.appendOutput(ln.FinalText)
			}
			w.cur.bump(ts)
			w.flush()
		}
	}
	w.flush()
	out.Spans = w.spans
	return out, nil
}

// droidMessageLine folds one "message" record in either dialect.
func droidMessageLine(w *turnWalk, ln *droidLine, ts int64) {
	// Exec stream: role + text at the top level. Skip streaming deltas — the
	// final message repeats their content.
	if ln.Role != "" {
		if normType(ln.Subtype) == "delta" || ln.Text == "" {
			return
		}
		switch normType(ln.Role) {
		case "user":
			// Strip BEFORE opening: a message that is pure harness scaffolding
			// must not create an empty-prompt turn.
			prompt := stripSystemReminder(ln.Text)
			if prompt == "" {
				return
			}
			w.open(ts)
			w.cur.prompt = prompt
		case "assistant":
			w.ensure(ts)
			w.cur.appendOutput(ln.Text)
			w.cur.bump(ts)
		}
		return
	}

	// Session store: nested message object with claude-style parts.
	var msg droidMessage
	if err := json.Unmarshal(ln.Message, &msg); err != nil {
		return
	}
	switch normType(msg.Role) {
	case "user":
		droidUserParts(w, msg.Content, ts)
	case "assistant":
		w.ensure(ts)
		for _, b := range msg.Content {
			switch normType(b.Type) {
			case "text":
				if b.Text != "" {
					w.cur.appendOutput(b.Text)
				}
			case "tooluse":
				args := compactJSON(b.Input)
				w.cur.addCommandTool(b.Name, b.ID, args, commandFromArgs(b.Input), ts, w.sessionID, nil)
			}
		}
		w.cur.bump(ts)
	}
}

// droidUserParts handles a session-store user message: tool_result parts close
// pending tools (they are outputs, not a new prompt); text parts open a turn.
func droidUserParts(w *turnWalk, parts []droidBlock, ts int64) {
	var text string
	var results []droidBlock
	for _, b := range parts {
		switch normType(b.Type) {
		case "toolresult":
			results = append(results, b)
		case "text":
			if b.Text != "" {
				if text != "" {
					text += "\n"
				}
				text += b.Text
			}
		}
	}
	if len(results) > 0 && w.cur != nil {
		for _, res := range results {
			w.cur.applyResult(res.ToolUseID, droidValueText(res.Content), ts, false)
		}
		return
	}
	prompt := stripSystemReminder(text)
	if prompt == "" {
		return
	}
	w.open(ts)
	w.cur.prompt = prompt
}

// droidValueText renders a tool result value (string or object) to text.
func droidValueText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}
