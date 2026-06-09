package derive

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/astra-sh/qvr/internal/ops"
)

func init() { Register("codex", codexDeriver{}) }

// codexDeriver reconstructs the Turn→Tool/Skill hierarchy from a Codex CLI
// rollout transcript. Like the claude deriver it is PURE — the same stored
// lines always rebuild the same spans — so the projection is regenerable.
//
// Codex's rollout JSONL is a flat stream of records, each a top-level
// {"timestamp","type","payload"} envelope. Four envelope types matter:
//
//	session_meta  — once per session (model provider, cwd, cli version)
//	turn_context  — per turn: carries the model used for the turn
//	event_msg     — harness events; payload.type is the subtype:
//	                  task_started   (turn opens)
//	                  user_message   (the real prompt — clean, no injected context)
//	                  token_count    (per-request usage; info.last_token_usage)
//	                  agent_message  (final assistant text)
//	                  task_complete  (turn closes)
//	response_item — model/tool I/O; payload.type is the subtype:
//	                  message            (role user/developer = injected context;
//	                                      role assistant = output text)
//	                  function_call      (a tool call: name, arguments, call_id)
//	                  function_call_output (its result, keyed by call_id)
//
// The prompt is taken from the event_msg "user_message" event rather than the
// response_item user messages, because the latter also carry injected context
// (AGENTS.md, environment_context, the developer instructions block) that is
// not what the user typed.
//
// Skill attribution uses Codex's OWN native mechanism, not anything Quiver-
// specific: Codex injects a <skills_instructions> block (a developer message)
// listing every available skill with its name + SKILL.md path, and tells the
// model to "open its SKILL.md" to use it. So a skill use surfaces as the model
// reading a file under a `skills/<name>/` (or `rules/<name>/`) directory — e.g.
// `sed -n 1,40p .codex/skills/code-review/SKILL.md`. We parse the injected list
// for the authoritative set of skill names, then attribute any tool command
// that touches one of those skill paths. This does not depend on `qvr` being on
// the PATH or on a `qvr read` call — it is exactly the signal Codex itself
// defines, so it keeps working for skills installed by any tool.
type codexDeriver struct{}

// codexLine is the rollout envelope. Payload is left raw and decoded per type.
type codexLine struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

// codexPayload is the union of the payload fields we read across envelope types.
// Only the fields relevant to the current envelope are populated; the rest stay
// zero.
type codexPayload struct {
	Type string `json:"type"` // event_msg / response_item subtype

	// turn_context (and, when present, session_meta)
	Model         string `json:"model"`
	ModelProvider string `json:"model_provider"`

	// response_item: message
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // []{type,text}

	// response_item: function_call
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string of the call args
	CallID    string `json:"call_id"`

	// response_item: function_call_output
	Output json.RawMessage `json:"output"` // string (usually) or object

	// event_msg: user_message / agent_message
	Message string `json:"message"`

	// event_msg: task_complete
	LastAgentMessage string `json:"last_agent_message"`

	// event_msg: token_count
	Info *codexTokenInfo `json:"info"`
}

type codexTokenInfo struct {
	Last codexUsage `json:"last_token_usage"`
}

type codexUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// codexBlock is one content block of a message (input_text / output_text).
type codexBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (codexDeriver) Derive(rows []*ops.RawTrace) ([]Span, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	st := &codexState{
		sessionID: rows[0].SessionID.String(),
		// valid is the authoritative set of skill names Codex injected via
		// <skills_instructions> for this session. Empty until that block is seen;
		// while empty, skill-path detection falls back to accepting any
		// well-formed skills/<name> segment (a still-native signal).
		valid: map[string]bool{},
	}

	for _, r := range rows {
		if r.Source != ops.RawSourceTranscript {
			continue
		}
		var ln codexLine
		if err := json.Unmarshal(r.Raw, &ln); err != nil {
			continue
		}
		ts := parseCodexTime(ln.Timestamp)
		var p codexPayload
		if len(ln.Payload) > 0 {
			_ = json.Unmarshal(ln.Payload, &p)
		}

		switch ln.Type {
		case "session_meta", "turn_context":
			st.handleModel(p)
		case "event_msg":
			st.handleEventMsg(p, ts)
		case "response_item":
			st.handleResponseItem(p, ts)
		}
	}
	st.flush()
	return st.spans, nil
}

// codexState carries the mutable walk state while Derive folds rollout records
// into spans: the accumulating spans, the open turn (if any), the running turn
// index, the most recent model seen, and the learned skill-name set.
type codexState struct {
	sessionID string
	spans     []Span
	cur       *turn
	turnIdx   int
	model     string          // most recent model seen (session_meta / turn_context)
	valid     map[string]bool // skill names injected via <skills_instructions>
}

// openTurn starts a fresh turn at ts using the most recent model.
func (st *codexState) openTurn(ts int64) {
	st.turnIdx++
	tid := traceID(st.sessionID, "turn", strconv.Itoa(st.turnIdx))
	st.cur = &turn{
		index:     st.turnIdx,
		startMs:   ts,
		endMs:     ts,
		model:     st.model,
		traceID:   tid,
		llmSpanID: spanID(tid, "llm"),
		pending:   map[string]int{},
	}
}

// flush emits the open turn's LLM + tool spans and clears it.
func (st *codexState) flush() {
	if st.cur == nil {
		return
	}
	st.spans = append(st.spans, st.cur.llmSpan(st.sessionID))
	st.spans = append(st.spans, st.cur.tools...)
	st.cur = nil
}

// ensure opens a turn at ts when none is currently open.
func (st *codexState) ensure(ts int64) {
	if st.cur == nil {
		st.openTurn(ts)
	}
}

// handleModel records the turn's model. session_meta usually only has
// model_provider; turn_context carries the concrete model id.
func (st *codexState) handleModel(p codexPayload) {
	if p.Model != "" {
		st.model = p.Model
		if st.cur != nil {
			st.cur.model = st.model
		}
	}
}

// handleEventMsg processes an event_msg envelope: turn open/close, the clean
// prompt, accumulated usage, and the final assistant text.
func (st *codexState) handleEventMsg(p codexPayload, ts int64) {
	switch p.Type {
	case "task_started":
		st.flush()
		st.openTurn(ts)
	case "user_message":
		st.ensure(ts)
		if st.cur.prompt == "" {
			st.cur.prompt = p.Message
		}
	case "agent_message":
		st.ensure(ts)
		if p.Message != "" {
			st.cur.appendOutput(p.Message)
		}
		st.cur.bump(ts)
	case "token_count":
		if st.cur != nil && p.Info != nil {
			st.cur.inTokens += p.Info.Last.InputTokens
			st.cur.outTokens += p.Info.Last.OutputTokens
		}
	case "task_complete":
		st.ensure(ts)
		if st.cur.output == "" && p.LastAgentMessage != "" {
			st.cur.appendOutput(p.LastAgentMessage)
		}
		st.cur.bump(ts)
		st.flush()
	}
}

// handleResponseItem processes a response_item envelope: assistant output text,
// the injected <skills_instructions> registry, tool calls, and their results.
func (st *codexState) handleResponseItem(p codexPayload, ts int64) {
	switch p.Type {
	case "message":
		blocks := decodeCodexBlocks(p.Content)
		// Any message (usually the developer one) may carry the
		// <skills_instructions> registry; learn the skill names from it.
		for _, b := range blocks {
			if strings.Contains(b.Text, skillsInstructionsTag) {
				for name := range parseCodexSkills(b.Text) {
					st.valid[name] = true
				}
			}
		}
		// Only assistant output is the turn's text. User/developer
		// messages here are injected context, not the prompt.
		if p.Role == "assistant" {
			st.ensure(ts)
			for _, b := range blocks {
				if b.Text != "" {
					st.cur.appendOutput(b.Text)
				}
			}
			st.cur.bump(ts)
		}
	case "function_call":
		st.ensure(ts)
		st.cur.addCodexTool(p, ts, st.sessionID, st.valid)
		st.cur.bump(ts)
	case "function_call_output":
		if st.cur != nil {
			st.cur.applyCodexResult(p, ts)
		}
	}
}

// appendOutput accumulates assistant text, newline-separated.
func (t *turn) appendOutput(s string) {
	if t.output != "" {
		t.output += "\n"
	}
	t.output += s
}

// bump extends the turn's end time to ts when ts is later.
func (t *turn) bump(ts int64) {
	if ts > t.endMs {
		t.endMs = ts
	}
}

// addCodexTool turns a function_call into a child span. Skill usage is detected
// the way Codex itself defines it (see the type doc): a tool command that reads
// a file under a `skills/<name>/` (or `rules/<name>/`) path is attributed to
// that skill via the skill.name extension. Opening the skill's SKILL.md is the
// "load" and is lifted to a dedicated SKILL span (parity with claude's Skill
// tool-call); touching other files under the skill dir stays a TOOL span but
// still carries skill.name, so the action is attributed without inventing a
// load. valid is the authoritative skill-name set from <skills_instructions>.
func (t *turn) addCodexTool(p codexPayload, ts int64, sessionID string, valid map[string]bool) {
	args := map[string]any{}
	if p.Arguments != "" {
		_ = json.Unmarshal([]byte(p.Arguments), &args)
	}
	cmd := codexCommand(args)

	// A literal "Skill" tool-call wins; otherwise read the native path signal.
	skill := ops.SkillRefFromTool(p.Name, args)
	isLoad := skill != ""
	loadPath := ""
	if skill == "" {
		skill, isLoad, loadPath = codexSkillRef(cmd, valid)
	}

	attrs := map[string]any{
		"session.id":                 sessionID,
		"gen_ai.operation.name":      "execute_tool",
		"gen_ai.tool.name":           p.Name,
		"gen_ai.tool.call.id":        p.CallID,
		"gen_ai.tool.call.arguments": p.Arguments,
	}
	if cmd != "" {
		attrs["gen_ai.tool.description"] = clip(cmd, 200)
	}
	kind := KindTool
	idKind := "tool"
	if skill != "" {
		attrs["skill.name"] = skill // Quiver extension
		// The actual file path the command referenced. EnrichSkillIdentity uses
		// it to attribute the artifact that *loaded* rather than name-matching
		// the lock, so a shadowing eject isn't reported as the locked copy (#149).
		if loadPath != "" {
			attrs["skill.load_path"] = loadPath
		}
		if isLoad {
			kind = KindSkill
			idKind = "skill"
		}
	}

	// Fall back to a per-turn unique suffix when Codex omits call_id: an empty
	// suffix would make every call_id-less tool span in the turn collide on the
	// same span id. len(t.tools) is the index this span will occupy, so it's
	// stable and unique within the turn. Such a span can't correlate to its
	// output (the result has no call_id either), so it simply won't be attached.
	callKey := p.CallID
	if callKey == "" {
		callKey = "tool#" + strconv.Itoa(len(t.tools))
	}
	sp := Span{
		Name:         "execute_tool " + p.Name,
		Kind:         kind,
		SpanID:       spanID(t.traceID, idKind, callKey),
		TraceID:      t.traceID,
		ParentSpanID: t.llmSpanID,
		StartMs:      ts,
		EndMs:        ts,
		Attributes:   attrs,
	}
	t.tools = append(t.tools, sp)
	if p.CallID != "" {
		t.pending[p.CallID] = len(t.tools) - 1
	}
}

const skillsInstructionsTag = "<skills_instructions>"

// codexSkillEntryRe matches one entry of the injected skill list, e.g.
//
//   - code-review: Review pending changes ... (file: /path/.../skills/code-review/SKILL.md)
//
// capturing the skill name (the file path is implied by the name segment).
var codexSkillEntryRe = regexp.MustCompile(`(?m)^\s*-\s+([a-z0-9][a-z0-9-]{0,63}):.*\(file:`)

// codexSkillPathRe matches a skill-directory reference inside a shell command,
// capturing the whole path token (group 1) and the skill name (group 2). The
// `skills/<name>/` or `rules/<name>/` segment is shared by every install form —
// the absolute ~/.quiver worktree path and the relative agent-dir symlink both
// contain it — so the captured token is the real path the command referenced,
// which EnrichSkillIdentity resolves to verify the loaded artifact.
var codexSkillPathRe = regexp.MustCompile(`(\S*(?:skills|rules)/([a-z0-9][a-z0-9-]{0,63})(?:/\S*)?)`)

// parseCodexSkills extracts the skill names from a <skills_instructions> block.
func parseCodexSkills(text string) map[string]bool {
	out := map[string]bool{}
	for _, m := range codexSkillEntryRe.FindAllStringSubmatch(text, -1) {
		out[m[1]] = true
	}
	return out
}

// codexSkillRef reports the skill a shell command touches, whether the access
// is the skill's SKILL.md (its "load"), and the path token the command actually
// referenced (so identity can be verified against the loaded artifact, not just
// the name). When valid is non-empty only names Codex actually offered this
// session match; when empty (no injected list seen) any well-formed
// skills/<name> segment is accepted, which is still a native, qvr-independent
// signal.
func codexSkillRef(cmd string, valid map[string]bool) (name string, isLoad bool, loadPath string) {
	if cmd == "" {
		return "", false, ""
	}
	for _, m := range codexSkillPathRe.FindAllStringSubmatch(cmd, -1) {
		path, n := m[1], m[2]
		if len(valid) > 0 && !valid[n] {
			continue
		}
		return n, strings.Contains(cmd, n+"/SKILL.md"), path
	}
	return "", false, ""
}

// applyCodexResult attaches a function_call_output to the tool span awaiting it.
func (t *turn) applyCodexResult(p codexPayload, ts int64) {
	idx, ok := t.pending[p.CallID]
	if !ok {
		return
	}
	sp := &t.tools[idx]
	sp.Attributes["gen_ai.tool.call.result"] = decodeCodexOutput(p.Output)
	if ts > sp.StartMs {
		sp.EndMs = ts
	}
	delete(t.pending, p.CallID)
}

// codexCommand pulls the shell command out of an exec/shell tool's arguments.
// Codex names the field "cmd" (exec_command) or "command" (shell); the value is
// a string or a []string argv.
func codexCommand(args map[string]any) string {
	for _, k := range []string{"cmd", "command"} {
		switch v := args[k].(type) {
		case string:
			if v != "" {
				return v
			}
		case []any:
			parts := make([]string, 0, len(v))
			for _, e := range v {
				if s, ok := e.(string); ok {
					parts = append(parts, s)
				}
			}
			if len(parts) > 0 {
				return joinFields(parts)
			}
		}
	}
	return ""
}

func joinFields(parts []string) string {
	var out strings.Builder
	for i, p := range parts {
		if i > 0 {
			out.WriteString(" ")
		}
		out.WriteString(p)
	}
	return out.String()
}

func decodeCodexBlocks(raw json.RawMessage) []codexBlock {
	var blocks []codexBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	return blocks
}

// decodeCodexOutput renders a function_call_output payload (a string, or a
// {output:...} / {content:...} object) to text.
func decodeCodexOutput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var obj struct {
		Output  string `json:"output"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		if obj.Output != "" {
			return obj.Output
		}
		if obj.Content != "" {
			return obj.Content
		}
	}
	return string(raw)
}

// parseCodexTime parses a rollout ISO-8601 timestamp to epoch ms, or 0.
func parseCodexTime(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}
