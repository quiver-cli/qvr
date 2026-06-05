// Package codex is the first-party SkillOps hook installer for Codex CLI. It
// implements ops.HookInstaller over ~/.codex/hooks.json.
//
// Scope note: Quiver ships the hooks.json mode only. Codex CLI's alternate
// config.toml event-stream / --trace-fd mode is a documented deferred
// follow-up.
package codex

import (
	"github.com/quiver-cli/qvr/internal/ops"
)

// AgentName is the dispatch key: `qvr _hook codex <type>`.
const AgentName = "codex"

const displayName = "Codex CLI"

// Adapter implements ops.HookInstaller.
type Adapter struct{}

func (a *Adapter) Name() string        { return AgentName }
func (a *Adapter) DisplayName() string { return displayName }

func init() {
	ops.Register(&Adapter{})
}
