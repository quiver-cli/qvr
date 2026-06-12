package derive

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/ops"
	"github.com/google/uuid"
)

// Version is the deriver revision stamped on every persisted span. Bump it when
// the derivation logic changes meaningfully, so stored spans from an older
// deriver can be told apart and re-derived for parity comparison.
//
// v2: OpenTelemetry gen_ai.* semantic conventions (was OpenInference in v1).
// v3: skill.* identity (registry/version/commit/source/subtree_hash/canonical)
// resolved from qvr.lock via EnrichSkillIdentity (#146).
// v4: load-path-aware attribution — codex spans carry skill.load_path, and
// EnrichSkillIdentity asserts lock identity only when the loaded path proves it
// (stamping skill.verified); unprovable identity is withheld or flagged (#149).
// v5: unified session model — every derivation also constructs a SessionMeta
// (the one read model the UI/CLI/metrics list sessions from), and agents are
// keyed by canonical target name.
// v6: proof-gated identity — skill.verified is gone; identity fields are
// stamped only on load-path proof, and skill.version's presence IS the
// verified signal (always set on proof, short SHA when the entry has no ref).
// claude gains load paths: the harness-injected isMeta "Base directory for
// this skill:" line (observed in real session stores, 2026-06-11) plus the
// universal path signal over tool calls; isMeta lines no longer fabricate
// turns.
const Version = 6

// KindSkill is the Quiver span category for a skill load/invocation within a
// turn. It exists so the trace makes skill usage a first-class, queryable stage
// (the basis for skill attribution and evals) rather than burying it inside a
// generic tool span. On the wire it is an OTel execute_tool span carrying the
// skill.name extension attribute.
const KindSkill = "SKILL"

// SessionMeta is the unified session model — the per-session header every
// consumer (UI, CLI, metrics) reads. Derivers construct it from the session's
// verbatim raw rows, so like spans it is a deterministic, regenerable
// projection: same rows + same Version ⇒ same meta.
//
// A deriver only fills the fields that need format-specific parsing (e.g.
// GitBranch); finalizeMeta fills everything derivable generically from the
// rows and spans (identity, title, model, counts, time bounds).
type SessionMeta struct {
	SessionID       uuid.UUID // canonical correlation key (same as raw_traces)
	Agent           string    // canonical target name (model.CanonicalTarget)
	SourceSessionID string    // the agent's own session id, verbatim
	SourcePath      string    // native store file the session came from
	WorkingDir      string    // cwd (project scoping)
	GitBranch       string
	Model           string // LLM model (last seen across the session)
	Title           string // first real user prompt, one line, clipped
	StartedMs       int64  // epoch ms of the first span
	EndedMs         int64  // epoch ms of the last span end
	Turns           int64  // LLM span count
	Tools           int64  // TOOL span count
	Skills          []string
}

// Derivation is one session's full derived projection: the unified meta plus
// the Turn→Tool/Skill spans. Both come from the same walk over the same rows,
// so they always describe the same derivation.
type Derivation struct {
	Meta  SessionMeta
	Spans []Span
}

// Deriver turns one session's raw rows into its derived projection.
// Implementations are per-agent because each agent's transcript format
// differs; the registry maps canonical target name → Deriver. A deriver must
// be PURE: same rows in → same Derivation out (including span ids), so the
// projection is reproducible.
type Deriver interface {
	Derive(rows []*ops.RawTrace) (*Derivation, error)
}

var registry = map[string]Deriver{}

// Register installs a deriver for an agent, keyed by canonical target name.
// Called from deriver init().
func Register(agent string, d Deriver) { registry[canonicalAgent(agent)] = d }

// Get returns the deriver for an agent, or (nil, false). The name is
// normalized through the target registry, so aliases (and agent names stored
// by earlier qvr versions, e.g. "claude-code") resolve to the same deriver.
func Get(agent string) (Deriver, bool) {
	d, ok := registry[canonicalAgent(agent)]
	return d, ok
}

// Registered returns the canonical names of every agent with a deriver, sorted.
func Registered() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// canonicalAgent normalizes an agent name or alias to its canonical target
// name; an unknown name passes through unchanged.
func canonicalAgent(name string) string {
	if c, ok := model.CanonicalTarget(name); ok {
		return c
	}
	return name
}

// DeriveSession derives one session's projection from its rows. The agent is
// taken from the first row; an empty slice yields nil. Returns an error only
// if no deriver is registered for the agent.
func DeriveSession(rows []*ops.RawTrace) (*Derivation, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	agent := rows[0].AgentName
	d, ok := Get(agent)
	if !ok {
		return nil, fmt.Errorf("derive: no deriver registered for agent %q", agent)
	}
	out, err := d.Derive(rows)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = &Derivation{}
	}
	finalizeMeta(out, rows)
	return out, nil
}

// finalizeMeta fills every SessionMeta field derivable generically from the
// rows and spans, leaving any format-specific value a deriver already set
// (e.g. GitBranch) untouched. Keeping this shared is what lets per-agent
// derivers stay thin.
func finalizeMeta(d *Derivation, rows []*ops.RawTrace) {
	m := &d.Meta
	m.SessionID = rows[0].SessionID
	m.Agent = canonicalAgent(rows[0].AgentName)
	fillMetaIdentity(m, rows)
	if m.Title == "" {
		m.Title = firstPromptFromSpans(d.Spans, titleDefaultMaxLen)
	}
	fillMetaFromSpans(m, d.Spans)
	// Formats without in-file timestamps derive zero time bounds; fall back to
	// the capture times so the session still buckets into the right day.
	if m.StartedMs == 0 && len(rows) > 0 {
		m.StartedMs = rows[0].CapturedAt.UnixMilli()
	}
	if m.EndedMs < m.StartedMs {
		m.EndedMs = m.StartedMs
	}
}

// fillMetaIdentity recovers the session's source identity (agent session id,
// source file, cwd) from the first rows that carry each value.
func fillMetaIdentity(m *SessionMeta, rows []*ops.RawTrace) {
	for _, r := range rows {
		if m.SourceSessionID == "" {
			m.SourceSessionID = r.AgentSessionID
		}
		if m.SourcePath == "" {
			m.SourcePath = r.SourcePath
		}
		if m.WorkingDir == "" {
			m.WorkingDir = r.WorkingDirectory
		}
		if m.SourceSessionID != "" && m.SourcePath != "" && m.WorkingDir != "" {
			return
		}
	}
}

// fillMetaFromSpans accumulates the time bounds, model, turn/tool counts, and
// the distinct skill list (first-use order) from the derived spans.
func fillMetaFromSpans(m *SessionMeta, spans []Span) {
	seenSkill := map[string]bool{}
	for _, sp := range spans {
		if m.StartedMs == 0 || (sp.StartMs > 0 && sp.StartMs < m.StartedMs) {
			m.StartedMs = sp.StartMs
		}
		if end := max(sp.EndMs, sp.StartMs); end > m.EndedMs {
			m.EndedMs = end
		}
		switch sp.Kind {
		case KindLLM:
			m.Turns++
			if model, ok := sp.Attributes["gen_ai.request.model"].(string); ok && model != "" {
				m.Model = model
			}
		case KindTool:
			m.Tools++
		}
		if name, ok := sp.Attributes["skill.name"].(string); ok && name != "" && !seenSkill[name] {
			seenSkill[name] = true
			m.Skills = append(m.Skills, name)
		}
	}
}

// --- deterministic id helpers ---
//
// Spans are a regenerable projection, so ids are derived from stable content
// (session + turn + tool) rather than randomness: re-deriving the same rows
// reproduces the same trace/span ids. Format matches the reference (32-hex
// trace id, 16-hex span id) so any OTLP consumer accepts them.

func traceID(parts ...string) string { return hashHex(16, parts...) } // 16 bytes → 32 hex
func spanID(parts ...string) string  { return hashHex(8, parts...) }  // 8 bytes  → 16 hex

func hashHex(n int, parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:n])
}
