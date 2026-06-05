package derive

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/quiver-cli/qvr/internal/ops"
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
const Version = 4

// KindSkill is the Quiver span category for a skill load/invocation within a
// turn. It exists so the trace makes skill usage a first-class, queryable stage
// (the basis for skill attribution and evals) rather than burying it inside a
// generic tool span. On the wire it is an OTel execute_tool span carrying the
// skill.name extension attribute.
const KindSkill = "SKILL"

// Deriver turns one session's raw rows into spans. Implementations are
// per-agent because each agent's transcript format differs; the registry maps
// agent_name → Deriver. A deriver must be PURE: same rows in → same spans out
// (including ids), so the projection is reproducible.
type Deriver interface {
	Derive(rows []*ops.RawTrace) ([]Span, error)
}

var registry = map[string]Deriver{}

// Register installs a deriver for an agent. Called from deriver init().
func Register(agent string, d Deriver) { registry[agent] = d }

// Get returns the deriver for an agent, or (nil, false).
func Get(agent string) (Deriver, bool) {
	d, ok := registry[agent]
	return d, ok
}

// DeriveSession derives spans for a single session's rows. The agent is taken
// from the first row; an empty slice yields no spans. Returns an error only if
// no deriver is registered for the agent.
func DeriveSession(rows []*ops.RawTrace) ([]Span, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	agent := rows[0].AgentName
	d, ok := Get(agent)
	if !ok {
		return nil, fmt.Errorf("derive: no deriver registered for agent %q", agent)
	}
	return d.Derive(rows)
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
