// Package opencode is the first-party SkillOps hook installer for OpenCode.
// Unlike the JSON-hooks agents, OpenCode loads JS plugins, so installation
// drops an embedded quiver.js into ~/.config/opencode/plugins/ that shells out
// to `qvr _hook opencode <type>`.
package opencode

import (
	"github.com/quiver-cli/qvr/internal/ops"
)

// AgentName is the dispatch key: `qvr _hook opencode <type>`.
const AgentName = "opencode"

const displayName = "OpenCode"

// Adapter implements ops.HookInstaller.
type Adapter struct{}

func (a *Adapter) Name() string        { return AgentName }
func (a *Adapter) DisplayName() string { return displayName }

func init() {
	ops.Register(&Adapter{})
}
