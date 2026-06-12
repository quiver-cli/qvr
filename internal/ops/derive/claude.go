package derive

import (
	"encoding/json"
	"strings"

	"github.com/astra-sh/qvr/internal/ops"
)

func init() { Register("claude", claudeDeriver{}) }

// claudeDeriver reconstructs the Turn→Tool/Skill hierarchy from Claude Code
// transcript lines. Reconstruction (not a live hook stream) is what makes the
// projection regenerable: the same stored lines always rebuild the same spans.
//
// The semantic model is the standard agent-trace hierarchy: a user prompt opens
// a Turn (LLM span); assistant tool_use blocks become TOOL children;
// tool_result lines supply their output; a Skill tool-call is lifted into a
// dedicated SKILL span. It is derived from the full transcript, so it carries
// more than a hook-payload stream (e.g. reasoning, every assistant message).
//
// Skill-load evidence (verified against real ~/.claude/projects stores,
// 2026-06-11): a skill invocation is a tool_use named "Skill" with input
// {"skill":"<name>","args"?:"..."}; its tool_result is only the text
// "Launching skill: <name>" — no path. The load path arrives TWO LINES LATER
// as a type=user line flagged isMeta:true whose text block begins
//
//	Base directory for this skill: /abs/path/.claude/skills/<name>
//
// followed by the skill's SKILL.md body. That base-directory line is the
// artifact evidence: it is captured as skill.load_path on the pending SKILL
// span, which EnrichSkillIdentity resolves (symlink → worktree containment)
// to prove which locked artifact ran. isMeta lines are harness-injected
// content, NEVER user prompts — treating them as prompts both loses the load
// path and fabricates a turn whose "prompt" is the skill body. Transcripts
// also carry non-message line types (attachment, last-prompt, ai-title,
// mode); the type switch ignores them.
type claudeDeriver struct{}

// claudeLine is the subset of a Claude transcript JSONL line we read.
// gitBranch rides on every line (per Claude Code's transcript format) and
// feeds the unified session meta. isMeta marks harness-injected user lines
// (skill bodies, context attachments) as opposed to typed prompts.
type claudeLine struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	GitBranch string          `json:"gitBranch"`
	IsMeta    bool            `json:"isMeta"`
	Message   json.RawMessage `json:"message"`
}

type claudeMessage struct {
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"` // string OR []block
	Usage   claudeUsage     `json:"usage"`
}

type claudeUsage struct {
	InputTokens              int `json:"input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	OutputTokens             int `json:"output_tokens"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	Input     map[string]any  `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"` // tool_result content: string OR []block
	IsError   bool            `json:"is_error"`
}

func (claudeDeriver) Derive(rows []*ops.RawTrace) (*Derivation, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	w := &turnWalk{sessionID: rows[0].SessionID.String()}
	out := &Derivation{}

	for _, r := range rows {
		if r.Source != ops.RawSourceTranscript {
			continue
		}
		var ln claudeLine
		if err := json.Unmarshal(r.Raw, &ln); err != nil {
			continue // a non-JSON / unexpected line is skipped, never fatal
		}
		if out.Meta.GitBranch == "" && ln.GitBranch != "" {
			out.Meta.GitBranch = ln.GitBranch
		}
		ts := parseISOMs(ln.Timestamp)

		switch ln.Type {
		case "user":
			claudeUserLine(w, &ln, ts)
		case "assistant":
			// ensure covers assistant output with no preceding prompt (e.g.
			// a session resumed mid-turn): a synthetic turn, nothing lost.
			w.ensure(ts)
			w.cur.absorbAssistant(ln.Message, ts, w.sessionID)
		}
	}
	w.flush()
	out.Spans = w.spans
	return out, nil
}

// claudeUserLine folds one type=user line into the walk. A line bearing
// tool_result blocks is the OUTPUT of the current turn's pending tools, not a
// new prompt. Harness-injected content (isMeta) is never a prompt either —
// the one we mine is the skill-body injection: its leading "Base directory
// for this skill: <path>" line is the load-path evidence for the turn's
// pending SKILL span (see the type doc). Everything else is a real prompt and
// opens a new turn.
func claudeUserLine(w *turnWalk, ln *claudeLine, ts int64) {
	role, text, results := parseUserContent(ln.Message)
	if role == "" {
		return
	}
	if len(results) > 0 && w.cur != nil {
		for _, res := range results {
			w.cur.applyToolResult(res, ts)
		}
		return
	}
	if ln.IsMeta {
		if w.cur != nil {
			w.cur.applySkillBaseDir(text)
		}
		return
	}
	w.open(ts)
	w.cur.prompt = text
}

// absorbAssistant folds one assistant line into the current turn: appends text,
// sums tokens, records the model, and turns each tool_use block into a TOOL
// (or SKILL) child span.
func (t *turn) absorbAssistant(raw json.RawMessage, ts int64, sessionID string) {
	var msg claudeMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}
	if msg.Model != "" {
		t.model = msg.Model
	}
	t.inTokens += msg.Usage.InputTokens + msg.Usage.CacheReadInputTokens + msg.Usage.CacheCreationInputTokens
	t.outTokens += msg.Usage.OutputTokens
	if ts > t.endMs {
		t.endMs = ts
	}

	for _, b := range decodeBlocks(msg.Content) {
		switch b.Type {
		case "text":
			if b.Text != "" {
				if t.output != "" {
					t.output += "\n"
				}
				t.output += b.Text
			}
		case "tool_use":
			t.addToolSpan(b, ts, sessionID)
		}
	}
}

// addToolSpan creates a child span for a tool_use block. A skill invocation
// (the "Skill" tool) is lifted into a dedicated SKILL span recording the loaded
// skill name and load time; everything else is a TOOL span.
func (t *turn) addToolSpan(b claudeBlock, ts int64, sessionID string) {
	inputJSON := ""
	if b.Input != nil {
		if data, err := json.Marshal(b.Input); err == nil {
			inputJSON = string(data)
		}
	}

	// Both skill loads and ordinary tool calls are OTel "execute_tool" spans;
	// a skill load additionally carries the Quiver skill.name extension tag.
	if skill := ops.SkillRefFromTool(b.Name, b.Input); skill != "" {
		attrs := map[string]any{
			"session.id":                 sessionID,
			"gen_ai.operation.name":      "execute_tool",
			"gen_ai.tool.name":           b.Name,
			"gen_ai.tool.call.id":        b.ID,
			"gen_ai.tool.call.arguments": inputJSON,
			"skill.name":                 skill, // Quiver extension
		}
		if t.model != "" {
			attrs["gen_ai.request.model"] = t.model // model cut for skill aggregations
		}
		sp := Span{
			Name:         "execute_tool " + b.Name,
			Kind:         KindSkill,
			SpanID:       spanID(t.traceID, "skill", b.ID),
			TraceID:      t.traceID,
			ParentSpanID: t.llmSpanID,
			StartMs:      ts,
			EndMs:        ts,
			Attributes:   attrs,
		}
		t.tools = append(t.tools, sp)
		return
	}

	attrs := map[string]any{
		"session.id":                 sessionID,
		"gen_ai.operation.name":      "execute_tool",
		"gen_ai.tool.name":           b.Name,
		"gen_ai.tool.call.id":        b.ID,
		"gen_ai.tool.call.arguments": inputJSON,
	}
	if t.model != "" {
		attrs["gen_ai.request.model"] = t.model
	}
	if d := toolDescription(b); d != "" {
		attrs["gen_ai.tool.description"] = d
	}
	// Universal path signal: a Read/Bash/etc. that touches a file under a
	// skill directory attributes the action to that skill — and when it opens
	// the skill's SKILL.md, that's a load with path evidence (the same signal
	// the codex/cursor/copilot derivers use). This is how supporting-file
	// reads (references/, scripts/) of a skill become verifiable on claude.
	kind, idKind := KindTool, "tool"
	name, isLoad, loadPath := pathSkillRef(commandFromArgs(b.Input), nil)
	if name == "" {
		name, isLoad, loadPath = pathSkillRef(inputJSON, nil)
	}
	if name != "" {
		attrs["skill.name"] = name
		if loadPath != "" {
			attrs["skill.load_path"] = loadPath
		}
		if isLoad {
			kind, idKind = KindSkill, "skill"
		}
	}
	sp := Span{
		Name:         "execute_tool " + b.Name,
		Kind:         kind,
		SpanID:       spanID(t.traceID, idKind, b.ID),
		TraceID:      t.traceID,
		ParentSpanID: t.llmSpanID,
		StartMs:      ts,
		EndMs:        ts,
		Attributes:   attrs,
	}
	t.tools = append(t.tools, sp)
	if b.ID != "" {
		t.pending[b.ID] = len(t.tools) - 1
	}
}

// claudeSkillBaseDirPrefix opens the harness-injected skill body (see the
// type doc): the remainder of its first line is the loaded skill's directory.
const claudeSkillBaseDirPrefix = "Base directory for this skill: "

// applySkillBaseDir mines a harness-injected (isMeta) user text for the
// "Base directory for this skill: <path>" line and attaches the path as
// skill.load_path on the turn's most recent SKILL span that lacks one — the
// injection observed in real stores arrives immediately after the Skill
// tool_result, inside the same turn.
func (t *turn) applySkillBaseDir(text string) {
	if !strings.HasPrefix(text, claudeSkillBaseDirPrefix) {
		return
	}
	path := text[len(claudeSkillBaseDirPrefix):]
	if i := strings.IndexByte(path, '\n'); i >= 0 {
		path = path[:i]
	}
	path = strings.TrimSpace(path)
	// The base-dir path encodes the skill name (…/skills/<name>); pass it so
	// two Skill calls in one assistant message (parallel tool use) each get
	// their OWN injection — a nameless reverse search would swap them. A path
	// with no skills/<name> segment yields "" and matches any pending span.
	name, _, _ := pathSkillRef(path, nil)
	t.attachSkillLoadPath(name, path)
}

// applyToolResult attaches a tool_result to the tool span awaiting it.
func (t *turn) applyToolResult(b claudeBlock, ts int64) {
	idx, ok := t.pending[b.ToolUseID]
	if !ok {
		return
	}
	sp := &t.tools[idx]
	sp.Attributes["gen_ai.tool.call.result"] = decodeToolResultText(b.Content)
	if b.IsError {
		sp.Attributes["error.type"] = "tool_failure"
	}
	if ts > sp.StartMs {
		sp.EndMs = ts
	}
	delete(t.pending, b.ToolUseID)
}

// llmSpan renders the turn's parent span — an OTel GenAI "chat" inference span.
func (t *turn) llmSpan(sessionID string) Span {
	output := t.output
	if output == "" {
		output = "(no text output)"
	}
	inMsgs, _ := json.Marshal([]map[string]string{{"role": "user", "content": t.prompt}})
	outMsgs, _ := json.Marshal([]map[string]string{{"role": "assistant", "content": output}})
	end := max(t.endMs, t.startMs)
	name := "chat"
	if t.model != "" {
		name = "chat " + t.model
	}
	attrs := map[string]any{
		"session.id":                 sessionID,
		"gen_ai.operation.name":      "chat",
		"gen_ai.request.model":       t.model,
		"gen_ai.usage.input_tokens":  t.inTokens,
		"gen_ai.usage.output_tokens": t.outTokens,
		"gen_ai.input.messages":      string(inMsgs),
		"gen_ai.output.messages":     string(outMsgs),
	}
	if p := providerName(t.model); p != "" {
		attrs["gen_ai.provider.name"] = p
	}
	return Span{
		Name:       name,
		Kind:       KindLLM,
		SpanID:     t.llmSpanID,
		TraceID:    t.traceID,
		StartMs:    t.startMs,
		EndMs:      end,
		Attributes: attrs,
	}
}

// providerName maps a model id to its OTel gen_ai.provider.name, or "" when
// unknown.
func providerName(model string) string {
	switch {
	case strings.HasPrefix(model, "claude"):
		return "anthropic"
	case strings.HasPrefix(model, "gpt"), strings.HasPrefix(model, "o1"), strings.HasPrefix(model, "o3"):
		return "openai"
	case strings.HasPrefix(model, "gemini"):
		return "gcp.gemini"
	default:
		return ""
	}
}

// --- content decoding helpers ---

// parseUserContent classifies a user message. Returns (role, promptText,
// toolResults). promptText is set when the content is a plain prompt; results
// is set when the content carries tool_result blocks.
func parseUserContent(raw json.RawMessage) (role, text string, results []claudeBlock) {
	var msg claudeMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return "", "", nil
	}
	role = msg.Role
	if role == "" {
		role = "user"
	}
	// content as a plain string → a prompt.
	var s string
	if err := json.Unmarshal(msg.Content, &s); err == nil {
		return role, s, nil
	}
	// content as an array → text blocks (prompt) and/or tool_result blocks.
	for _, b := range decodeBlocks(msg.Content) {
		switch b.Type {
		case "tool_result":
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
	return role, text, results
}

func decodeBlocks(raw json.RawMessage) []claudeBlock {
	var blocks []claudeBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	return blocks
}

// decodeToolResultText renders a tool_result's content (string or block array)
// to text.
func decodeToolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	out := ""
	for _, b := range decodeBlocks(raw) {
		if b.Type == "text" && b.Text != "" {
			if out != "" {
				out += "\n"
			}
			out += b.Text
		}
	}
	return out
}

// toolDescription gives a short human label per tool, mirroring the reference's
// per-tool description logic.
func toolDescription(b claudeBlock) string {
	get := func(k string) string {
		if v, ok := b.Input[k].(string); ok {
			return v
		}
		return ""
	}
	switch b.Name {
	case "Bash":
		return clip(get("command"), 200)
	case "Read", "Write", "Edit", "Glob":
		if p := get("file_path"); p != "" {
			return clip(p, 200)
		}
		return clip(get("pattern"), 200)
	case "Grep":
		return clip("grep: "+get("pattern"), 200)
	case "WebFetch":
		return clip(get("url"), 200)
	case "WebSearch":
		return clip(get("query"), 200)
	default:
		return ""
	}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
