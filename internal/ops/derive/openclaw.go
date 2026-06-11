package derive

import (
	"encoding/json"
	"strings"

	"github.com/astra-sh/qvr/internal/ops"
)

func init() { Register("openclaw", openclawDeriver{}) }

// openclawDeriver reconstructs the Turn→Tool/Skill hierarchy from an OpenClaw
// session transcript — an append-only JSONL of typed records (documented at
// https://docs.openclaw.ai/concepts/session):
//
//	{"type":"session","version":3,"id":"…","timestamp":"…","cwd":"…"}
//	{"type":"message","id","parentId","timestamp","message":{role, content[]}}
//	{"type":"modelchange","modelId":"…","provider":"…"}
//
// Message roles: "user" opens a turn; "assistant" carries text blocks plus
// toolCall blocks {type:"toolCall",id,name,arguments}; "toolResult" closes a
// pending tool via toolCallId. Skill attribution is the shared path signal:
// a tool invocation touching a skills/<name>/ path is attributed to that
// skill (its SKILL.md read is the load).
type openclawDeriver struct{}

// openclawLine is the typed record envelope.
type openclawLine struct {
	Type      string          `json:"type"`
	Timestamp json.RawMessage `json:"timestamp"`
	ModelID   string          `json:"modelId"`
	Message   json.RawMessage `json:"message"`
}

// openclawMessage is the message body of a "message" record.
type openclawMessage struct {
	Role       string          `json:"role"`
	Model      string          `json:"model"`
	ToolCallID string          `json:"toolCallId"`
	Content    json.RawMessage `json:"content"` // string OR []block
}

// openclawBlock is one content block.
type openclawBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text"`
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func (openclawDeriver) Derive(rows []*ops.RawTrace) (*Derivation, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	w := &turnWalk{sessionID: rows[0].SessionID.String()}
	out := &Derivation{}

	for _, r := range rows {
		if r.Source != ops.RawSourceTranscript {
			continue
		}
		var ln openclawLine
		if err := json.Unmarshal(r.Raw, &ln); err != nil {
			continue
		}
		ts := flexTimeMs(ln.Timestamp)

		switch normType(ln.Type) {
		case "modelchange":
			w.setModel(ln.ModelID)
		case "message":
			var msg openclawMessage
			if err := json.Unmarshal(ln.Message, &msg); err != nil {
				continue
			}
			handleBlockMessage(w, blockMsg{
				role:       msg.Role,
				model:      msg.Model,
				toolCallID: msg.ToolCallID,
				content:    ln.Message, // unused; blocks decoded below
				text:       openclawText(msg.Content),
				blocks:     decodeOpenclawBlocks(msg.Content),
			}, ts)
		}
	}
	w.flush()
	out.Spans = w.spans
	return out, nil
}

// blockMsg is the normalized shape of one block-structured chat message — the
// common core of the OpenClaw and Pi formats.
type blockMsg struct {
	role       string
	model      string
	toolCallID string
	content    json.RawMessage
	text       string
	blocks     []openclawBlock
}

// handleBlockMessage folds one normalized message into the walk: user text
// opens a turn, assistant text/tool blocks extend it, tool results close
// pending tools.
func handleBlockMessage(w *turnWalk, m blockMsg, ts int64) {
	w.setModel(m.model)
	switch normType(m.role) {
	case "user":
		prompt := stripSystemReminder(m.text)
		if prompt == "" {
			return
		}
		w.open(ts)
		w.cur.prompt = prompt
	case "assistant":
		w.ensure(ts)
		if m.text != "" {
			w.cur.appendOutput(m.text)
		}
		for _, b := range m.blocks {
			if normType(b.Type) == "toolcall" {
				args := compactJSON(b.Arguments)
				w.cur.addCommandTool(b.Name, b.ID, args, commandFromArgs(b.Arguments), ts, w.sessionID, nil)
			}
		}
		w.cur.bump(ts)
	case "toolresult":
		if w.cur != nil && m.toolCallID != "" {
			w.cur.applyResult(m.toolCallID, m.text, ts, false)
		}
	}
}

// openclawText flattens a content value (string or block array) to its text.
func openclawText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var sb strings.Builder
	for _, b := range decodeOpenclawBlocks(raw) {
		if normType(b.Type) == "text" && b.Text != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

func decodeOpenclawBlocks(raw json.RawMessage) []openclawBlock {
	var blocks []openclawBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	return blocks
}
