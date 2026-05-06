// Package ops implements the SkillOps audit pipeline: event model,
// attribution, privacy-gated persistence, and query surface.
//
// Ops is feature-gated. On a fresh install, Enabled() returns false and
// the hook funnel is a silent no-op. The user opts in via `qvr ops
// enable`, which sets config.Ops.Enabled=true, creates the SQLite
// database, and applies migrations.
package ops

import (
	"github.com/raks097/quiver/internal/config"
)

// Enabled returns whether the audit pipeline is active. Callers should
// short-circuit on false to keep the disabled path allocation-free.
//
// Nil cfg is treated as disabled (safe default for callers that can't
// load config).
func Enabled(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	return cfg.Ops.Enabled
}

// EnabledForAgent returns whether ops is enabled for a specific agent.
// Per-agent config can opt out individually even when ops is globally
// enabled; it cannot opt in when ops is globally disabled (the global
// flag is the on/off switch for the DB file itself).
func EnabledForAgent(cfg *config.Config, agentName string) bool {
	if !Enabled(cfg) {
		return false
	}
	if cfg.Ops.Agents == nil {
		return true
	}
	override, ok := cfg.Ops.Agents[agentName]
	if !ok {
		return true
	}
	return override.Enabled
}
