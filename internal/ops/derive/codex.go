package derive

import (
	"encoding/json"
	"regexp"
	"strings"

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

	// session_meta: repo context (feeds the unified session meta)
	Git *codexGitInfo `json:"git"`

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

// codexGitInfo is the session_meta payload's git block.
type codexGitInfo struct {
	Branch string `json:"branch"`
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

func (codexDeriver) Derive(rows []*ops.RawTrace) (*Derivation, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	st := &codexState{
		turnWalk: turnWalk{sessionID: rows[0].SessionID.String()},
		// valid is the authoritative set of skill names Codex injected via
		// <skills_instructions> for this session. Empty until that block is seen;
		// while empty, skill-path detection falls back to accepting any
		// well-formed skills/<name> segment (a still-native signal).
		valid: map[string]bool{},
	}
	out := &Derivation{}

	for _, r := range rows {
		if r.Source != ops.RawSourceTranscript {
			continue
		}
		var ln codexLine
		if err := json.Unmarshal(r.Raw, &ln); err != nil {
			continue
		}
		ts := parseISOMs(ln.Timestamp)
		var p codexPayload
		if len(ln.Payload) > 0 {
			_ = json.Unmarshal(ln.Payload, &p)
		}

		switch ln.Type {
		case "session_meta", "turn_context":
			st.setModel(p.Model)
			if out.Meta.GitBranch == "" && p.Git != nil && p.Git.Branch != "" {
				out.Meta.GitBranch = p.Git.Branch
			}
		case "event_msg":
			st.handleEventMsg(p, ts)
		case "response_item":
			st.handleResponseItem(p, ts)
		}
	}
	st.flush()
	out.Spans = st.spans
	return out, nil
}

// codexState is the shared turn walk plus the learned skill-name set.
type codexState struct {
	turnWalk
	valid map[string]bool // skill names injected via <skills_instructions>
}

// handleEventMsg processes an event_msg envelope: turn open/close, the clean
// prompt, accumulated usage, and the final assistant text.
func (st *codexState) handleEventMsg(p codexPayload, ts int64) {
	switch p.Type {
	case "task_started":
		st.open(ts)
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
			st.cur.applyResult(p.CallID, decodeCodexOutput(p.Output), ts, false)
		}
	}
}

// addCodexTool turns a function_call into a child span. Skill usage is detected
// the way Codex itself defines it (see the type doc): a tool command that reads
// a file under a `skills/<name>/` (or `rules/<name>/`) path is attributed to
// that skill via the skill.name extension; a literal "Skill" tool-call wins.
// valid is the authoritative skill-name set from <skills_instructions>.
func (t *turn) addCodexTool(p codexPayload, ts int64, sessionID string, valid map[string]bool) {
	args := map[string]any{}
	if p.Arguments != "" {
		_ = json.Unmarshal([]byte(p.Arguments), &args)
	}
	cmd := commandFromArgs(args)
	ref := resolveSkillRef(p.Name, args, cmd, "", valid)
	t.addToolInvocation(p.Name, p.CallID, p.Arguments, cmd, ref, ts, sessionID)
}

const skillsInstructionsTag = "<skills_instructions>"

// codexSkillEntryRe matches one entry of the injected skill list, e.g.
//
//   - code-review: Review pending changes ... (file: /path/.../skills/code-review/SKILL.md)
//
// capturing the skill name (the file path is implied by the name segment).
var codexSkillEntryRe = regexp.MustCompile(`(?m)^\s*-\s+([a-z0-9][a-z0-9-]{0,63}):.*\(file:`)

// parseCodexSkills extracts the skill names from a <skills_instructions> block.
func parseCodexSkills(text string) map[string]bool {
	out := map[string]bool{}
	for _, m := range codexSkillEntryRe.FindAllStringSubmatch(text, -1) {
		out[m[1]] = true
	}
	return out
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
