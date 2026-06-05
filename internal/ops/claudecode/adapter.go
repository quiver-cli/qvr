// Package claudecode is the first-party SkillOps hook installer for Claude
// Code. It implements ops.HookInstaller, wiring `qvr _hook claude-code <type>`
// into ~/.claude/settings.json. Capture itself is parser-free — the hook tails
// the agent's transcript verbatim (see internal/ops/rawtrace).
package claudecode

import (
	"github.com/quiver-cli/qvr/internal/ops"
)

// AgentName is the dispatch key: `qvr _hook claude-code <type>` and
// `qvr audit install-hooks --agent claude-code`.
const AgentName = "claude-code"

// displayName is the human-facing label.
const displayName = "Claude Code"

// Adapter implements ops.HookInstaller. It holds no state.
type Adapter struct{}

// Name satisfies ops.HookInstaller.
func (a *Adapter) Name() string { return AgentName }

// DisplayName satisfies ops.HookInstaller.
func (a *Adapter) DisplayName() string { return displayName }

func init() {
	ops.Register(&Adapter{})
}
