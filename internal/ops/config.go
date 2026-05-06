package ops

import (
	"github.com/raks097/quiver/internal/config"
)

// Logging levels. The value is stored as a string in config for
// readability; the constants here are the canonical set.
const (
	LoggingLevelMinimal  = "minimal"
	LoggingLevelStandard = "standard"
	LoggingLevelFull     = "full"
)

// DefaultRetentionDays is how long events linger before a `qvr ops db
// purge` will sweep them. 90 days is a middle-ground: long enough for
// weekly-review workflows, short enough that the DB doesn't balloon on
// a dev machine.
const DefaultRetentionDays = 90

// Default values for the logging truncation caps. Applied when the
// user hasn't set a value (zero) AND ApplyDefaults has been called.
// They bound the blast radius of an agent that writes a 10MB diff; the
// full content is still stored when LoggingLevelFull is selected.
const (
	DefaultStdoutMaxChars  = 500
	DefaultStderrMaxChars  = 500
	DefaultContextMaxChars = 2000
)

// ApplyDefaults fills missing values in cfg.Ops with conservative
// defaults. Safe to call multiple times — set fields are preserved.
//
// Called by commands that load config (e.g. `qvr ops enable`, the
// `_hook` funnel) before the config is handed to the pipeline.
func ApplyDefaults(cfg *config.Config) {
	if cfg == nil {
		return
	}
	if cfg.Ops.RetentionDays == 0 {
		cfg.Ops.RetentionDays = DefaultRetentionDays
	}
	if cfg.Ops.Logging.Level == "" {
		cfg.Ops.Logging.Level = LoggingLevelStandard
	}
	if cfg.Ops.Logging.StdoutMaxChars == 0 {
		cfg.Ops.Logging.StdoutMaxChars = DefaultStdoutMaxChars
	}
	if cfg.Ops.Logging.StderrMaxChars == 0 {
		cfg.Ops.Logging.StderrMaxChars = DefaultStderrMaxChars
	}
	if cfg.Ops.Logging.ContextMaxChars == 0 {
		cfg.Ops.Logging.ContextMaxChars = DefaultContextMaxChars
	}
}

// AgentLoggingLevel returns the effective logging level for the given
// agent: per-agent override if set, otherwise the global level,
// otherwise LoggingLevelStandard.
func AgentLoggingLevel(cfg *config.Config, agentName string) string {
	if cfg == nil {
		return LoggingLevelStandard
	}
	if cfg.Ops.Agents != nil {
		if override, ok := cfg.Ops.Agents[agentName]; ok && override.LoggingLevel != "" {
			return override.LoggingLevel
		}
	}
	if cfg.Ops.Logging.Level != "" {
		return cfg.Ops.Logging.Level
	}
	return LoggingLevelStandard
}
