// Package copilot is the first-party SkillOps hook installer for GitHub Copilot
// CLI. It implements ops.HookInstaller over a standalone hook file at
// ~/.copilot/hooks/quiver.json (Copilot loads every *.json in its hooks dir, so
// Quiver owns its own file outright).
//
// Modelled on the GitHub Copilot CLI hooks docs. Observe-only: the hook never
// emits a permissionDecision, so auditing can never block the user's tools
// (Copilot hooks run synchronously and CAN block; capture writes only to the
// store, never to stdout).
package copilot

import (
	"github.com/quiver-cli/qvr/internal/ops"
)

// AgentName is the dispatch key: `qvr _hook copilot <type>`.
const AgentName = "copilot"

const displayName = "GitHub Copilot CLI"

// Adapter implements ops.HookInstaller.
type Adapter struct{}

func (a *Adapter) Name() string        { return AgentName }
func (a *Adapter) DisplayName() string { return displayName }

func init() {
	ops.Register(&Adapter{})
}
