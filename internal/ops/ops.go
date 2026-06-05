// Package ops is the SkillOps audit surface: the feature gate, hook-installer
// registry, and shared paths for the raw trace store.
//
// Ops is feature-gated. On a fresh install, Enabled() returns false and the
// hook capturer is a silent no-op. The user opts in via `qvr audit enable`,
// which sets config.Ops.Enabled=true, creates the SQLite database, and applies
// migrations.
package ops

import (
	"github.com/quiver-cli/qvr/internal/config"
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
