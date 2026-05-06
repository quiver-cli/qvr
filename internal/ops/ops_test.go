package ops

import (
	"testing"

	"github.com/raks097/quiver/internal/config"
)

func TestEnabled_NilConfigReturnsFalse(t *testing.T) {
	if Enabled(nil) {
		t.Errorf("nil config should be disabled")
	}
}

func TestEnabled_ReflectsGlobalFlag(t *testing.T) {
	cfg := &config.Config{}
	if Enabled(cfg) {
		t.Errorf("default zero config should be disabled")
	}
	cfg.Ops.Enabled = true
	if !Enabled(cfg) {
		t.Errorf("enabled flag should flip Enabled()")
	}
}

func TestEnabledForAgent_RespectsGlobalDisable(t *testing.T) {
	cfg := &config.Config{}
	cfg.Ops.Enabled = false
	cfg.Ops.Agents = map[string]config.OpsAgentConfig{
		"claude": {Enabled: true},
	}
	if EnabledForAgent(cfg, "claude") {
		t.Errorf("per-agent enable cannot override global disable")
	}
}

func TestEnabledForAgent_DefaultIsOn(t *testing.T) {
	cfg := &config.Config{}
	cfg.Ops.Enabled = true
	if !EnabledForAgent(cfg, "claude") {
		t.Errorf("no per-agent entry should default to enabled when global is on")
	}
}

func TestEnabledForAgent_PerAgentDisable(t *testing.T) {
	cfg := &config.Config{}
	cfg.Ops.Enabled = true
	cfg.Ops.Agents = map[string]config.OpsAgentConfig{
		"cursor": {Enabled: false},
	}
	if EnabledForAgent(cfg, "cursor") {
		t.Errorf("per-agent disable should take effect")
	}
	if !EnabledForAgent(cfg, "claude") {
		t.Errorf("other agents still enabled")
	}
}

func TestApplyDefaults_FillsMissing(t *testing.T) {
	cfg := &config.Config{}
	cfg.Ops.Enabled = true
	ApplyDefaults(cfg)

	if cfg.Ops.RetentionDays != DefaultRetentionDays {
		t.Errorf("RetentionDays=%d want %d", cfg.Ops.RetentionDays, DefaultRetentionDays)
	}
	if cfg.Ops.Logging.Level != LoggingLevelStandard {
		t.Errorf("Level=%q want %q", cfg.Ops.Logging.Level, LoggingLevelStandard)
	}
	if cfg.Ops.Logging.StdoutMaxChars != DefaultStdoutMaxChars {
		t.Errorf("StdoutMaxChars=%d want %d", cfg.Ops.Logging.StdoutMaxChars, DefaultStdoutMaxChars)
	}
}

func TestApplyDefaults_PreservesExplicitValues(t *testing.T) {
	cfg := &config.Config{}
	cfg.Ops.RetentionDays = 30
	cfg.Ops.Logging.Level = LoggingLevelFull
	cfg.Ops.Logging.StdoutMaxChars = 42
	ApplyDefaults(cfg)

	if cfg.Ops.RetentionDays != 30 {
		t.Errorf("explicit RetentionDays overwritten")
	}
	if cfg.Ops.Logging.Level != LoggingLevelFull {
		t.Errorf("explicit Level overwritten")
	}
	if cfg.Ops.Logging.StdoutMaxChars != 42 {
		t.Errorf("explicit StdoutMaxChars overwritten")
	}
}

func TestApplyDefaults_NilSafety(t *testing.T) {
	ApplyDefaults(nil) // must not panic
}

func TestAgentLoggingLevel_Priority(t *testing.T) {
	cfg := &config.Config{}
	cfg.Ops.Logging.Level = LoggingLevelStandard
	cfg.Ops.Agents = map[string]config.OpsAgentConfig{
		"claude": {Enabled: true, LoggingLevel: LoggingLevelFull},
	}

	if got := AgentLoggingLevel(cfg, "claude"); got != LoggingLevelFull {
		t.Errorf("per-agent override should win; got %q", got)
	}
	if got := AgentLoggingLevel(cfg, "cursor"); got != LoggingLevelStandard {
		t.Errorf("fallback to global; got %q", got)
	}
}

func TestAgentLoggingLevel_NilConfigReturnsStandard(t *testing.T) {
	if got := AgentLoggingLevel(nil, "claude"); got != LoggingLevelStandard {
		t.Errorf("nil config should return standard; got %q", got)
	}
}

func TestDBPath_DefaultLocation(t *testing.T) {
	t.Setenv("QUIVER_HOME", "/tmp/test-quiver-home")
	got := DBPath(nil)
	want := "/tmp/test-quiver-home/skillops.db"
	if got != want {
		t.Errorf("DBPath=%q want %q", got, want)
	}
}

func TestDBPath_Override(t *testing.T) {
	cfg := &config.Config{}
	cfg.Ops.DBPath = "/custom/path/ops.db"
	if got := DBPath(cfg); got != "/custom/path/ops.db" {
		t.Errorf("override ignored; got %q", got)
	}
}
