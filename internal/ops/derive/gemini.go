package derive

import (
	"bytes"
	"encoding/json"

	"github.com/astra-sh/qvr/internal/ops"
)

func init() { Register("gemini", geminiDeriver{}) }

// geminiDeriver reconstructs the Turn→Tool/Skill hierarchy from a Gemini CLI
// session checkpoint (~/.gemini/tmp/<project-hash>/chats/session-*.json(l);
// see https://github.com/google-gemini/gemini-cli). The store is
// document-layout, so a session usually arrives as ONE raw row holding the
// whole file: either a wrapper object {"sessionId","model","startTime",
// "messages"|"history"|"items":[…]} or a flat array/JSONL of message items.
//
// Each item:
//
//	{"role"|"type":"user"|"gemini"|"model"|"assistant",
//	 "content":"…" | [{"text":"…"}], "parts":[{"text":"…"}],
//	 "timestamp"|"ts"|"created_at": ISO8601 or epoch,
//	 "toolCalls"|"tool_calls":[{"name"|"tool"|"displayName","id",
//	     "args"|"input":{…},
//	     "result":[{"functionResponse":{"response":{"output"|"stdout"|…}}}],
//	     "resultDisplay"|"output":"…"}]}
//
// Tool call and result ride the same item, so results attach immediately.
// Skill attribution is the shared path signal over each call's arguments.
//
// Gemini has NO skill-load event (observed live, 2026-06-11): skills are
// injected eagerly into the session context (an instructions block whose
// <available_resources> lists the skill directory), there is no skill tool,
// and SKILL.md itself is never read at use time. The observable footprint of
// "this skill ran" is the first tool call touching the skill's files — so the
// session's FIRST path-attributed call per skill is lifted to a SKILL span
// (later touches stay TOOL), keeping invocation counts comparable with agents
// that do have a discrete load event.
type geminiDeriver struct{}

// geminiState is the walk plus the per-session set of skills already lifted
// to a SKILL span (the first-touch-is-the-load rule above).
type geminiState struct {
	turnWalk
	skillLoaded map[string]bool
}

// geminiDoc is the wrapper-object form.
type geminiDoc struct {
	Model     string            `json:"model"`
	StartTime json.RawMessage   `json:"startTime"`
	Messages  []json.RawMessage `json:"messages"`
	History   []json.RawMessage `json:"history"`
	Items     []json.RawMessage `json:"items"`
}

// geminiItem is one message item.
type geminiItem struct {
	Type       string           `json:"type"`
	Role       string           `json:"role"`
	Model      string           `json:"model"`
	Timestamp  json.RawMessage  `json:"timestamp"`
	TS         json.RawMessage  `json:"ts"`
	CreatedAt  json.RawMessage  `json:"created_at"`
	Time       json.RawMessage  `json:"time"`
	Content    json.RawMessage  `json:"content"` // string or []{text}
	Text       string           `json:"text"`
	Parts      []geminiPart     `json:"parts"`
	ToolCalls  []geminiToolCall `json:"toolCalls"`
	ToolCalls2 []geminiToolCall `json:"tool_calls"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiToolCall struct {
	Name          string          `json:"name"`
	Tool          string          `json:"tool"`
	DisplayName   string          `json:"displayName"`
	ID            string          `json:"id"`
	Args          map[string]any  `json:"args"`
	Input         map[string]any  `json:"input"`
	Result        []geminiToolRes `json:"result"`
	ResultDisplay string          `json:"resultDisplay"`
	Output        string          `json:"output"`
}

type geminiToolRes struct {
	FunctionResponse *struct {
		Response map[string]any `json:"response"`
	} `json:"functionResponse"`
	Output string `json:"output"`
	Stdout string `json:"stdout"`
}

func (geminiDeriver) Derive(rows []*ops.RawTrace) (*Derivation, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	st := &geminiState{
		turnWalk:    turnWalk{sessionID: rows[0].SessionID.String()},
		skillLoaded: map[string]bool{},
	}
	out := &Derivation{}

	for _, r := range rows {
		if r.Source != ops.RawSourceTranscript {
			continue
		}
		items, fallbackMs := geminiItems(r.Raw, &st.turnWalk)
		for _, raw := range items {
			var item geminiItem
			if err := json.Unmarshal(raw, &item); err != nil {
				continue
			}
			geminiMessage(st, &item, fallbackMs)
		}
	}
	st.flush()
	out.Spans = st.spans
	return out, nil
}

// geminiItems flattens one raw row into message items, handling the wrapper
// object, the flat JSON array, and line-delimited forms. Wrapper/session
// metadata feeds the walk (model) and the returned fallback timestamp
// (startTime) stamps items that carry no time of their own.
func geminiItems(raw []byte, w *turnWalk) ([]json.RawMessage, int64) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, 0
	}
	if trimmed[0] == '[' {
		var arr []json.RawMessage
		if err := json.Unmarshal(trimmed, &arr); err == nil {
			return arr, 0
		}
		return nil, 0
	}
	if trimmed[0] == '{' {
		var doc geminiDoc
		if err := json.Unmarshal(trimmed, &doc); err == nil {
			w.setModel(doc.Model)
			start := flexTimeMs(doc.StartTime)
			for _, items := range [][]json.RawMessage{doc.Messages, doc.History, doc.Items} {
				if len(items) > 0 {
					return items, start
				}
			}
		}
		// A bare object with no message array may itself be one item, or the
		// row may be JSONL with one object per line.
		if !bytes.ContainsRune(trimmed, '\n') {
			return []json.RawMessage{trimmed}, 0
		}
	}
	var out []json.RawMessage
	for line := range bytes.SplitSeq(trimmed, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			out = append(out, json.RawMessage(line))
		}
	}
	return out, 0
}

// geminiMessage folds one item into the walk. fallbackMs (the document's
// startTime) stamps items that carry no timestamp of their own.
func geminiMessage(st *geminiState, item *geminiItem, fallbackMs int64) {
	st.setModel(item.Model)
	ts := firstFlexTime(item.TS, item.Timestamp, item.CreatedAt, item.Time)
	if ts == 0 {
		ts = fallbackMs
	}

	switch geminiRole(item) {
	case "user":
		text := geminiText(item)
		if text == "" {
			return
		}
		st.open(ts)
		st.cur.prompt = text
	case "assistant":
		st.ensure(ts)
		if text := geminiText(item); text != "" {
			st.cur.appendOutput(text)
		}
		calls := item.ToolCalls
		if len(calls) == 0 {
			calls = item.ToolCalls2
		}
		for _, tc := range calls {
			geminiTool(st, tc, ts)
		}
		st.cur.bump(ts)
	}
}

// geminiTool emits one tool call (and its inline result) as a span. The
// session's first call attributed to a skill is promoted to its SKILL span
// (gemini has no discrete load event — see the type doc).
func geminiTool(st *geminiState, tc geminiToolCall, ts int64) {
	name := firstNonEmptyStr(tc.Name, tc.Tool, tc.DisplayName)
	if name == "" && tc.ID == "" {
		return
	}
	args := tc.Args
	if args == nil {
		args = tc.Input
	}
	cmdText := commandFromArgs(args)
	argsJSON := compactJSON(args)
	ref := resolveSkillRef(name, args, cmdText, argsJSON, nil)
	if ref.name != "" && !st.skillLoaded[ref.name] {
		st.skillLoaded[ref.name] = true
		ref.isLoad = true // first touch IS the load on gemini
	}
	st.cur.addToolInvocation(name, tc.ID, argsJSON, cmdText, ref, ts, st.sessionID)
	if result := geminiToolOutput(tc); result != "" {
		st.cur.applyResultLast(result, ts, false)
	}
}

// geminiToolOutput renders a call's inline result: the functionResponse
// payload first, then the display fields.
func geminiToolOutput(tc geminiToolCall) string {
	for _, r := range tc.Result {
		if r.FunctionResponse != nil {
			for _, k := range []string{"output", "stdout", "text", "content"} {
				if s, ok := r.FunctionResponse.Response[k].(string); ok && s != "" {
					return s
				}
			}
		}
		if r.Output != "" {
			return r.Output
		}
		if r.Stdout != "" {
			return r.Stdout
		}
	}
	return firstNonEmptyStr(tc.ResultDisplay, tc.Output)
}

// geminiRole normalizes the item's role/type to user|assistant|other.
func geminiRole(item *geminiItem) string {
	r := normType(firstNonEmptyStr(item.Role, item.Type))
	switch r {
	case "user", "human":
		return "user"
	case "gemini", "model", "assistant":
		return "assistant"
	default:
		return r
	}
}

// geminiText flattens the item's content/text/parts to a single string.
func geminiText(item *geminiItem) string {
	if item.Text != "" {
		return item.Text
	}
	if len(item.Content) > 0 {
		var s string
		if err := json.Unmarshal(item.Content, &s); err == nil {
			return s
		}
		var parts []geminiPart
		if err := json.Unmarshal(item.Content, &parts); err == nil {
			return joinParts(parts)
		}
	}
	return joinParts(item.Parts)
}

func joinParts(parts []geminiPart) string {
	out := ""
	for _, p := range parts {
		if p.Text == "" {
			continue
		}
		if out != "" {
			out += "\n"
		}
		out += p.Text
	}
	return out
}

// firstFlexTime returns the first parseable timestamp among candidates.
func firstFlexTime(candidates ...json.RawMessage) int64 {
	for _, c := range candidates {
		if ts := flexTimeMs(c); ts != 0 {
			return ts
		}
	}
	return 0
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
