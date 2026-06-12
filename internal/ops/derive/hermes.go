package derive

import (
	"encoding/json"

	"github.com/astra-sh/qvr/internal/ops"
)

func init() { Register("hermes", hermesDeriver{}) }

// hermesDeriver reconstructs the Turn→Tool/Skill hierarchy from a Hermes
// Agent session document (~/.hermes/sessions/session_*.json; see
// https://hermes-agent.nousresearch.com/docs/developer-guide/session-storage).
// The document layout delivers the whole session as one raw row:
//
//	{"session_id","model","platform","session_start","last_updated",
//	 "cwd"?, "model_config":{"cwd"?},
//	 "messages":[{"role":"user"|"assistant"|"tool","content":"…",
//	   "tool_calls":[{"id","function":{"name","arguments"}}],
//	   "tool_call_id"?, "tool_name"?, "reasoning"?}]}
//
// Current Hermes versions persist sessions in SQLite (~/.hermes/state.db),
// captured by rawtrace.IngestHermesStateDB as FLAT rows — one "type":"session"
// header (model, cwd, title, token totals) plus one "type":"message" row per
// messages-table row ({role, content, tool_calls, tool_call_id, timestamp
// epoch-seconds}). Both shapes share hermesMessageWalk; the flat shape is
// detected per row by its "type" envelope (observed live 2026-06-11). Skill
// attribution: hermes's native skill tool is "skill_view" (name-keyed — see
// ops.SkillRefFromTool) whose RESULT inlines the loaded skill's directory
// ("skill_dir": "~/.hermes/skills/<name>") — mined as the span's load path
// by the shared result miner; terminal commands touching
// ~/.hermes/skills/<name>/… paths feed the shared path signal too.
type hermesDeriver struct{}

// hermesFlatRow is the state.db capture envelope (header or message row).
type hermesFlatRow struct {
	Type       string           `json:"type"`
	Model      string           `json:"model"`
	Title      string           `json:"title"`
	Role       string           `json:"role"`
	Content    json.RawMessage  `json:"content"`
	ToolCalls  []hermesToolCall `json:"tool_calls"`
	ToolCallID string           `json:"tool_call_id"`
	Timestamp  json.RawMessage  `json:"timestamp"` // epoch seconds (REAL)
}

// hermesDoc is the session document.
type hermesDoc struct {
	Model        string          `json:"model"`
	SessionStart string          `json:"session_start"`
	LastUpdated  string          `json:"last_updated"`
	Messages     []hermesMessage `json:"messages"`
}

// hermesMessage is one chat message.
type hermesMessage struct {
	Role       string           `json:"role"`
	Content    json.RawMessage  `json:"content"` // string or blocks
	ToolCalls  []hermesToolCall `json:"tool_calls"`
	ToolCallID string           `json:"tool_call_id"`
}

// hermesToolCall is an OpenAI-style function call.
type hermesToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON string
	} `json:"function"`
}

func (hermesDeriver) Derive(rows []*ops.RawTrace) (*Derivation, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	w := &turnWalk{sessionID: rows[0].SessionID.String()}
	out := &Derivation{}

	for _, r := range rows {
		if r.Source != ops.RawSourceTranscript {
			continue
		}
		// state.db capture: flat header/message rows (vs the legacy whole-
		// document row, which has no "type" envelope).
		var flat hermesFlatRow
		if err := json.Unmarshal(r.Raw, &flat); err == nil && flat.Type != "" {
			switch flat.Type {
			case "session":
				w.setModel(flat.Model)
			case "message":
				hermesMessageWalk(w, hermesMessage{
					Role:       flat.Role,
					Content:    flat.Content,
					ToolCalls:  flat.ToolCalls,
					ToolCallID: flat.ToolCallID,
				}, flexTimeMs(flat.Timestamp))
			}
			continue
		}
		var doc hermesDoc
		if err := json.Unmarshal(r.Raw, &doc); err != nil {
			continue
		}
		w.setModel(doc.Model)
		// The document carries one session-level time range; per-message times
		// are not recorded. Stamp messages with the start (day bucketing stays
		// right) and close the final turn at last_updated, so the session's
		// derived duration matches the document's own range instead of zero.
		ts := parseISOMs(doc.SessionStart)
		for _, msg := range doc.Messages {
			hermesMessageWalk(w, msg, ts)
		}
		if w.cur != nil {
			w.cur.bump(parseISOMs(doc.LastUpdated))
		}
	}
	w.flush()
	out.Spans = w.spans
	return out, nil
}

// hermesMessageWalk folds one message into the walk.
func hermesMessageWalk(w *turnWalk, msg hermesMessage, ts int64) {
	switch normType(msg.Role) {
	case "user":
		prompt := stripSystemReminder(rawToText(msg.Content))
		if prompt == "" {
			return
		}
		w.open(ts)
		w.cur.prompt = prompt
	case "assistant":
		w.ensure(ts)
		if text := rawToText(msg.Content); text != "" {
			w.cur.appendOutput(text)
		}
		for _, tc := range msg.ToolCalls {
			w.cur.addCommandTool(tc.Function.Name, tc.ID, tc.Function.Arguments,
				hermesCommand(tc.Function.Arguments), ts, w.sessionID, nil)
		}
		w.cur.bump(ts)
	case "tool":
		if w.cur != nil && msg.ToolCallID != "" {
			w.cur.applyResult(msg.ToolCallID, rawToText(msg.Content), ts, false)
		}
	}
}

// hermesCommand extracts the shell command from a function call's JSON
// argument string.
func hermesCommand(arguments string) string {
	if arguments == "" {
		return ""
	}
	args := map[string]any{}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return ""
	}
	return commandFromArgs(args)
}
