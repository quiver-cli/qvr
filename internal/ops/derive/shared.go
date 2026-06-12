package derive

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/astra-sh/qvr/internal/ops"
)

// Shared format helpers for the per-agent derivers. Every deriver walks a
// different native record shape, but they all need the same primitives:
// flexible timestamp parsing, the path-based skill signal, and the
// turn → tool/skill span plumbing. Keeping these here is what keeps each
// deriver a thin format adapter.

// parseISOMs parses an ISO-8601 timestamp to epoch ms, or 0.
func parseISOMs(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// flexTimeMs normalizes the timestamp encodings agent transcripts use — an
// ISO-8601 string, or a numeric epoch in seconds, milliseconds, or
// microseconds — to epoch ms.
func flexTimeMs(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return parseISOMs(s)
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err != nil || n <= 0 {
		return 0
	}
	switch {
	case n > 1e15: // microseconds
		return int64(n / 1e3)
	case n > 1e12: // milliseconds
		return int64(n)
	default: // seconds
		return int64(n * 1e3)
	}
}

// skillDirPathRe matches a skill-directory reference inside any text (a shell
// command, serialized tool arguments, a file path), capturing the whole path
// token (group 1) and the skill name (group 2). The `skills/<name>/` or
// `rules/<name>/` segment is shared by every install form — the absolute
// ~/.quiver worktree path and the relative agent-dir symlink both contain it —
// so the captured token is the real path the tool referenced, which
// EnrichSkillIdentity resolves to verify the loaded artifact.
//
// The token classes exclude JSON syntax (quotes, braces, commas, backslashes)
// in addition to whitespace: paths are routinely matched inside compact
// serialized tool arguments ({"file_path":"/x/skills/y/SKILL.md"}), where a
// \S* token would swallow the surrounding JSON and yield an unresolvable
// "path" (observed in real claude stores, 2026-06-11).
var skillDirPathRe = regexp.MustCompile(`([^\s"'\\{}\[\],]*(?:skills|rules)/([a-z0-9][a-z0-9-]{0,63})(?:/[^\s"'\\{}\[\],]*)?)`)

// quiverWorktreePathRe matches a direct reference into qvr's immutable store:
// .quiver/worktrees/<registry>/<skill>/<sha7>/... — capturing the whole token
// (group 1) and the skill name (group 2). Agents resolve the agent-dir symlink
// before reading (observed in real codex rollouts, 2026-06-11: `sed -n 1,220p
// /Users/u/.quiver/worktrees/_local/qvr-probe/17dd2d4/SKILL.md`), and a LOCAL
// install's subtree has no skills/<name>/ segment for skillDirPathRe to find,
// so the store layout itself must be a recognized signal. It is also the
// strongest one: the path pins registry+skill+sha directly.
var quiverWorktreePathRe = regexp.MustCompile(`([^\s"'\\{}\[\],]*\.quiver/worktrees/[^/\s"'\\{}\[\],]+/([a-z0-9][a-z0-9-]{0,63})/[0-9a-f]{7}(?:/[^\s"'\\{}\[\],]*)?)`)

// pathSkillRef reports the skill a tool invocation touches, whether the access
// is the skill's SKILL.md (its "load"), and the path token actually
// referenced. When valid is non-empty only those names match (an agent that
// announces its skill set, like codex); when empty any well-formed
// skills/<name> segment is accepted — still a native, qvr-independent signal.
func pathSkillRef(text string, valid map[string]bool) (name string, isLoad bool, loadPath string) {
	if text == "" {
		return "", false, ""
	}
	// The store-layout signal first: a resolved worktree path is unambiguous
	// (registry/skill/sha in the path) and is NOT gated on the valid set —
	// a path into qvr's own store identifies the skill regardless of what the
	// agent announced.
	for _, m := range quiverWorktreePathRe.FindAllStringSubmatch(text, -1) {
		path, n := m[1], m[2]
		return n, strings.HasSuffix(path, "/SKILL.md"), path
	}
	for _, m := range skillDirPathRe.FindAllStringSubmatch(text, -1) {
		path, n := m[1], m[2]
		if len(valid) > 0 && !valid[n] {
			continue
		}
		return n, strings.Contains(text, n+"/SKILL.md"), path
	}
	return "", false, ""
}

// skillRef is one resolved skill attribution for a tool invocation.
type skillRef struct {
	name     string
	isLoad   bool   // the invocation opened the skill's SKILL.md (its "load")
	loadPath string // the path token actually referenced, for verification
}

// resolveSkillRef attributes one tool invocation to a skill: a literal "Skill"
// tool-call wins (the agent's own first-class skill mechanism); otherwise the
// path signal over the invocation's command text, then its serialized
// arguments. valid optionally restricts the accepted skill-name set. When the
// caller has only serialized arguments (OpenAI-style function calls carry a
// JSON string — hermes, codex), they are parsed here so name-keyed skill
// tools like hermes's skill_view still resolve.
func resolveSkillRef(toolName string, args map[string]any, cmdText, argsJSON string, valid map[string]bool) skillRef {
	if args == nil && argsJSON != "" {
		args = map[string]any{}
		_ = json.Unmarshal([]byte(argsJSON), &args)
	}
	if name := ops.SkillRefFromTool(toolName, args); name != "" {
		// A skill tool invoked WITH a file argument reads a supporting file
		// (hermes's skill_view(name, file_path)) — attribute it to the skill
		// without counting a load, mirroring how path-signal file reads stay
		// TOOL spans.
		return skillRef{name: name, isLoad: !skillToolReadsFile(args)}
	}
	name, isLoad, loadPath := pathSkillRef(cmdText, valid)
	if name == "" {
		name, isLoad, loadPath = pathSkillRef(argsJSON, valid)
	}
	return skillRef{name: name, isLoad: isLoad, loadPath: loadPath}
}

// skillToolReadsFile reports whether a skill tool-call's arguments target a
// supporting file rather than the skill itself.
func skillToolReadsFile(args map[string]any) bool {
	for _, k := range []string{"file_path", "file"} {
		if v, ok := args[k].(string); ok && v != "" {
			return true
		}
	}
	return false
}

// addToolInvocation turns one tool invocation into a child span. A skill load
// (ref.isLoad) lifts to a SKILL span; touching other files under a skill dir
// stays a TOOL span that still carries skill.name, so the action is attributed
// without inventing a load.
func (t *turn) addToolInvocation(toolName, callID, argsJSON, cmdText string, ref skillRef, ts int64, sessionID string) {
	attrs := map[string]any{
		"session.id":                 sessionID,
		"gen_ai.operation.name":      "execute_tool",
		"gen_ai.tool.name":           toolName,
		"gen_ai.tool.call.id":        callID,
		"gen_ai.tool.call.arguments": argsJSON,
	}
	// The turn's model rides on its tool/skill children so skill aggregations
	// can cut by model ("skill A on opus vs skill B on fable") without a
	// parent-span join.
	if t.model != "" {
		attrs["gen_ai.request.model"] = t.model
	}
	if cmdText != "" {
		attrs["gen_ai.tool.description"] = clip(cmdText, 200)
	}
	kind := KindTool
	idKind := "tool"
	if ref.name != "" {
		attrs["skill.name"] = ref.name // Quiver extension
		// The actual file path the invocation referenced; EnrichSkillIdentity
		// uses it to attribute the artifact that loaded rather than
		// name-matching the lock (#149).
		if ref.loadPath != "" {
			attrs["skill.load_path"] = ref.loadPath
		}
		if ref.isLoad {
			kind = KindSkill
			idKind = "skill"
		}
	}

	// Fall back to a per-turn unique suffix when the format omits a call id:
	// an empty suffix would make every id-less tool span in the turn collide.
	callKey := callID
	if callKey == "" {
		callKey = "tool#" + strconv.Itoa(len(t.tools))
	}
	sp := Span{
		Name:         "execute_tool " + toolName,
		Kind:         kind,
		SpanID:       spanID(t.traceID, idKind, callKey),
		TraceID:      t.traceID,
		ParentSpanID: t.llmSpanID,
		StartMs:      ts,
		EndMs:        ts,
		Attributes:   attrs,
	}
	t.tools = append(t.tools, sp)
	if callID != "" {
		t.pending[callID] = len(t.tools) - 1
	}
}

// addCommandTool is the common-case wrapper: resolve the skill attribution
// from the invocation itself, then emit the span.
func (t *turn) addCommandTool(toolName, callID, argsJSON, cmdText string, ts int64, sessionID string, valid map[string]bool) {
	ref := resolveSkillRef(toolName, nil, cmdText, argsJSON, valid)
	t.addToolInvocation(toolName, callID, argsJSON, cmdText, ref, ts, sessionID)
}

// applyResult attaches a tool invocation's output to the span awaiting it.
func (t *turn) applyResult(callID, result string, ts int64, isError bool) {
	idx, ok := t.pending[callID]
	if !ok {
		return
	}
	applyResultTo(&t.tools[idx], result, ts, isError)
	delete(t.pending, callID)
}

// applyResultTo stamps a result onto one span. A SKILL span that lacked path
// evidence at call time gets a second chance here: name-only skill tools
// (hermes's skill_view, opencode's skill, …) inline the loaded artifact's
// real directory in their RESULT, so the result text is mined for it.
func applyResultTo(sp *Span, result string, ts int64, isError bool) {
	sp.Attributes["gen_ai.tool.call.result"] = result
	if isError {
		sp.Attributes["error.type"] = "tool_failure"
	}
	if ts > sp.StartMs {
		sp.EndMs = ts
	}
	mineSkillLoadPath(sp, result)
}

// mineSkillLoadPath extracts load-path evidence for a SKILL span from a tool
// result's text. Gated on the span's own skill.name so a result that mentions
// other skills' paths can never mis-attribute; the path is recorded only when
// the span has none yet (call-time evidence wins).
func mineSkillLoadPath(sp *Span, text string) {
	if sp.Kind != KindSkill || text == "" {
		return
	}
	if _, ok := sp.Attributes["skill.load_path"]; ok {
		return
	}
	name, _ := sp.Attributes["skill.name"].(string)
	if name == "" {
		return
	}
	// The worktree-path branch of pathSkillRef ignores the valid set, so the
	// returned name must be re-checked against the span's skill.
	if n, _, path := pathSkillRef(text, map[string]bool{name: true}); n == name && path != "" {
		// Some results spell the location as a file:// URI (opencode's
		// base-directory line); enrichment resolves filesystem paths.
		sp.Attributes["skill.load_path"] = strings.TrimPrefix(path, "file://")
	}
}

// attachSkillLoadPath sets skill.load_path on the turn's most recent SKILL
// span that lacks one — for agents whose load-path evidence arrives in a
// separate record after the skill invocation (claude's base-directory
// injection, copilot's skill.invoked event). A non-empty name additionally
// requires the span's skill.name to match.
func (t *turn) attachSkillLoadPath(name, path string) {
	if path == "" {
		return
	}
	for i := len(t.tools) - 1; i >= 0; i-- {
		sp := &t.tools[i]
		if sp.Kind != KindSkill {
			continue
		}
		if name != "" {
			if n, _ := sp.Attributes["skill.name"].(string); n != name {
				continue
			}
		}
		if _, ok := sp.Attributes["skill.load_path"]; ok {
			continue
		}
		sp.Attributes["skill.load_path"] = path
		return
	}
}

// turnWalk is the shared state machine the JSONL derivers drive: it owns the
// open turn, the running index, and the accumulated spans, so a deriver only
// translates its record shapes into openTurn/prompt/output/tool calls.
type turnWalk struct {
	sessionID string
	spans     []Span
	cur       *turn
	turnIdx   int
	model     string // most recent model seen; stamped on new turns
}

// open starts a fresh turn at ts (flushing any open one).
func (w *turnWalk) open(ts int64) {
	w.flush()
	w.turnIdx++
	tid := traceID(w.sessionID, "turn", strconv.Itoa(w.turnIdx))
	w.cur = &turn{
		index:     w.turnIdx,
		startMs:   ts,
		endMs:     ts,
		model:     w.model,
		traceID:   tid,
		llmSpanID: spanID(tid, "llm"),
		pending:   map[string]int{},
	}
}

// ensure opens a turn at ts when none is currently open (e.g. a session
// resumed mid-turn) so nothing is dropped.
func (w *turnWalk) ensure(ts int64) {
	if w.cur == nil {
		w.open(ts)
	}
}

// setModel records the model for the current and subsequent turns.
func (w *turnWalk) setModel(model string) {
	if model == "" {
		return
	}
	w.model = model
	if w.cur != nil {
		w.cur.model = model
	}
}

// flush emits the open turn's LLM + tool spans and clears it.
func (w *turnWalk) flush() {
	if w.cur == nil {
		return
	}
	w.spans = append(w.spans, w.cur.llmSpan(w.sessionID))
	w.spans = append(w.spans, w.cur.tools...)
	w.cur = nil
}

// systemReminderRe strips the leading harness-injected reminder block some
// agents prepend to the user's first prompt.
var systemReminderRe = regexp.MustCompile(`(?is)^\s*<system-reminder>.*?</system-reminder>\s*`)

// stripSystemReminder removes a leading <system-reminder> block from a prompt.
func stripSystemReminder(s string) string {
	return systemReminderRe.ReplaceAllString(s, "")
}

// compactJSON renders any decoded JSON value back to a compact string for the
// gen_ai.tool.call.arguments attribute.
func compactJSON(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// commandFromArgs pulls the shell command out of an exec/shell tool's
// arguments. Agents name the field "cmd" or "command"; the value is a string
// or a []string argv.
func commandFromArgs(args map[string]any) string {
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
				return strings.Join(parts, " ")
			}
		}
	}
	return ""
}

// turn accumulates one user→assistant exchange while we walk the transcript.
type turn struct {
	index     int
	startMs   int64
	endMs     int64
	prompt    string
	output    string
	model     string
	inTokens  int
	outTokens int
	traceID   string
	llmSpanID string
	tools     []Span         // TOOL + SKILL children, parented to llmSpanID
	pending   map[string]int // tool_use_id → index into tools (awaiting result)
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

// normType lowercases and strips separators so role/type spellings that vary
// across agents ("toolCall" / "tool_call" / "toolcall") compare equal.
func normType(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || r == '.' {
			return -1
		}
		return r
	}, strings.ToLower(s))
}
