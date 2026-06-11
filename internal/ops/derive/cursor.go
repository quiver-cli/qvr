package derive

import (
	"encoding/json"
	"strings"

	"github.com/astra-sh/qvr/internal/ops"
)

func init() { Register("cursor", cursorDeriver{}) }

// cursorDeriver reconstructs the Turn→Tool/Skill hierarchy from a Cursor agent
// transcript (JSONL under ~/.cursor/projects/<p>/agent-transcripts; see
// https://cursor.com/docs/cli/overview). Each line is a chat message with the
// role at the top level and content blocks nested under message:
//
//	{"role":"user"|"assistant"|"system","message":{"content":[
//	    {"type":"text","text":"…"},
//	    {"type":"thinking","thinking":"…"},
//	    {"type":"tool_use","id","name","input":{…}},
//	    {"type":"tool_result","tool_use_id","content"|"output":"…"}]}}
//
// Lines carry no timestamps; capture times stand in so ordering and day
// bucketing stay sane. The user's first prompt may be wrapped in
// <user_query> tags, which are stripped. Skill attribution is the shared
// path signal over each tool invocation.
type cursorDeriver struct{}

// cursorLine is one transcript message.
type cursorLine struct {
	Role    string `json:"role"`
	Message struct {
		Content []cursorBlock `json:"content"`
	} `json:"message"`
}

// cursorBlock is one content block. Tool fields tolerate the spelling
// variants the format uses (name/tool, content/output, tool_use/tool-call).
type cursorBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Tool      string          `json:"tool"`
	Input     json.RawMessage `json:"input"` // object or string
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"` // tool_result output
	Output    json.RawMessage `json:"output"`
	IsError   bool            `json:"is_error"`
}

func (cursorDeriver) Derive(rows []*ops.RawTrace) (*Derivation, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	w := &turnWalk{sessionID: rows[0].SessionID.String()}
	out := &Derivation{}

	for _, r := range rows {
		if r.Source != ops.RawSourceTranscript {
			continue
		}
		var ln cursorLine
		if err := json.Unmarshal(r.Raw, &ln); err != nil {
			continue
		}
		cursorMessage(w, &ln, r.CapturedAt.UnixMilli())
	}
	w.flush()
	out.Spans = w.spans
	return out, nil
}

// cursorMessage folds one message line into the walk.
func cursorMessage(w *turnWalk, ln *cursorLine, ts int64) {
	switch normType(ln.Role) {
	case "user":
		text, results := cursorSplitBlocks(ln.Message.Content)
		// tool_result blocks riding a user message are tool outputs, not a
		// new prompt.
		if len(results) > 0 && w.cur != nil {
			for _, res := range results {
				cursorApplyResult(w.cur, res, ts)
			}
			return
		}
		if text == "" {
			return
		}
		w.open(ts)
		w.cur.prompt = stripUserQueryTags(text)
	case "assistant":
		w.ensure(ts)
		for _, b := range ln.Message.Content {
			switch normType(b.Type) {
			case "text":
				if b.Text != "" {
					w.cur.appendOutput(b.Text)
				}
			case "tooluse", "toolcall":
				name := b.Name
				if name == "" {
					name = b.Tool
				}
				args := rawToText(b.Input)
				w.cur.addCommandTool(name, b.ID, args, "", ts, w.sessionID, nil)
			case "toolresult":
				cursorApplyResult(w.cur, b, ts)
			}
		}
		w.cur.bump(ts)
	}
}

// cursorSplitBlocks separates a user message's text from any tool_result
// blocks it carries.
func cursorSplitBlocks(blocks []cursorBlock) (text string, results []cursorBlock) {
	for _, b := range blocks {
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
	return text, results
}

// cursorApplyResult attaches a tool_result block to its pending tool — by id
// when present, else to the most recent invocation (the format sometimes
// omits ids and relies on sequence).
func cursorApplyResult(t *turn, b cursorBlock, ts int64) {
	result := rawToText(b.Content)
	if result == "" {
		result = rawToText(b.Output)
	}
	if b.ToolUseID != "" {
		t.applyResult(b.ToolUseID, result, ts, b.IsError)
		return
	}
	t.applyResultLast(result, ts, b.IsError)
}

// rawToText renders a raw JSON value (string or structure) to text.
func rawToText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

// stripUserQueryTags removes the literal <user_query> wrapper from a prompt.
func stripUserQueryTags(s string) string {
	s = strings.ReplaceAll(s, "<user_query>", "")
	s = strings.ReplaceAll(s, "</user_query>", "")
	return strings.TrimSpace(s)
}
