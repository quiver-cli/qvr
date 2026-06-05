// Package cursor is the first-party SkillOps hook installer for Cursor. It
// implements ops.HookInstaller, managing ~/.cursor/hooks.json. Capture is
// parser-free — the hook tails the agent's transcript verbatim (see
// internal/ops/rawtrace).
package cursor

import (
	"github.com/quiver-cli/qvr/internal/ops"
)

// AgentName is the dispatch key: `qvr _hook cursor <type>`.
const AgentName = "cursor"

const displayName = "Cursor"

// Adapter implements ops.HookInstaller.
type Adapter struct{}

func (a *Adapter) Name() string        { return AgentName }
func (a *Adapter) DisplayName() string { return displayName }

func init() {
	ops.Register(&Adapter{})
}
