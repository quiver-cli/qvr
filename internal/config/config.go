package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config represents the quiver configuration.
type Config struct {
	Registries      map[string]RegistryConfig `yaml:"registries,omitempty" json:"registries,omitempty"`
	DefaultTarget   string                    `yaml:"default_target,omitempty" json:"default_target,omitempty"`
	DefaultRegistry string                    `yaml:"default_registry,omitempty" json:"default_registry,omitempty"`
	GithubToken     string                    `yaml:"github_token,omitempty" json:"github_token,omitempty"`
	Security        SecurityConfig            `yaml:"security,omitempty" json:"security,omitempty"`
	Output          OutputConfig              `yaml:"output,omitempty" json:"output,omitempty"`
	Ops             OpsConfig                 `yaml:"ops,omitempty" json:"ops,omitempty"`
}

// OpsConfig controls the SkillOps audit pipeline. The struct lives here
// (not in internal/ops) so that internal/ops can depend on internal/config
// without a cycle. Defaults and behaviour live in internal/ops/config.go.
type OpsConfig struct {
	Enabled       bool                      `yaml:"enabled" json:"enabled"`
	DBPath        string                    `yaml:"db_path,omitempty" json:"db_path,omitempty"`
	RetentionDays int                       `yaml:"retention_days,omitempty" json:"retention_days,omitempty"`
	Logging       OpsLoggingConfig          `yaml:"logging,omitempty" json:"logging,omitempty"`
	Privacy       OpsPrivacyConfig          `yaml:"privacy,omitempty" json:"privacy,omitempty"`
	Agents        map[string]OpsAgentConfig `yaml:"agents,omitempty" json:"agents,omitempty"`
}

// OpsLoggingConfig tunes how much of an event's content survives the
// logging-level truncation stage. See internal/ops/logging_level.go.
type OpsLoggingConfig struct {
	Level           string `yaml:"level,omitempty" json:"level,omitempty"`                         // minimal|standard|full
	StdoutMaxChars  int    `yaml:"stdout_max_chars,omitempty" json:"stdout_max_chars,omitempty"`   // 0 = unlimited
	StderrMaxChars  int    `yaml:"stderr_max_chars,omitempty" json:"stderr_max_chars,omitempty"`   // 0 = unlimited
	ContextMaxChars int    `yaml:"context_max_chars,omitempty" json:"context_max_chars,omitempty"` // 0 = unlimited
	ContentHash     bool   `yaml:"content_hash,omitempty" json:"content_hash,omitempty"`
}

// OpsPrivacyConfig lets users extend the built-in privacy defaults.
// Defaults are always the floor — user patterns are merged on top.
type OpsPrivacyConfig struct {
	SensitivePaths []string `yaml:"sensitive_paths,omitempty" json:"sensitive_paths,omitempty"`
	RedactPatterns []string `yaml:"redact_patterns,omitempty" json:"redact_patterns,omitempty"`
}

// OpsAgentConfig overrides the global ops settings for a specific
// agent (e.g. log full events from claude, only minimal from cursor).
type OpsAgentConfig struct {
	Enabled      bool   `yaml:"enabled" json:"enabled"`
	LoggingLevel string `yaml:"logging_level,omitempty" json:"logging_level,omitempty"`
}

// RegistryConfig holds configuration for a single registry.
type RegistryConfig struct {
	URL string `yaml:"url" json:"url"`
}

// SecurityConfig holds security-related settings.
type SecurityConfig struct {
	ScanOnInstall bool   `yaml:"scan_on_install" json:"scan_on_install"`
	BlockSeverity string `yaml:"block_severity,omitempty" json:"block_severity,omitempty"`
}

// OutputConfig holds output preferences.
type OutputConfig struct {
	Format string `yaml:"format,omitempty" json:"format,omitempty"`
	Color  string `yaml:"color,omitempty" json:"color,omitempty"`
}

// Dir returns the quiver home directory. Override with QUIVER_HOME.
func Dir() string {
	if env := os.Getenv("QUIVER_HOME"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".quiver")
	}
	return filepath.Join(home, ".quiver")
}

// Path returns the path to the config file.
func Path() string {
	return filepath.Join(Dir(), "config.yaml")
}

// ParseDefaultTargets returns the agent targets implied by the
// default_target config value. Accepts a single name (e.g. "claude") or a
// comma-separated list ("claude,cursor") so users who routinely install
// into multiple agents don't need to pass --target every time. Trims
// whitespace, drops empty entries, returns nil for empty input.
//
// Comma support resolves the singular-vs-plural mismatch users hit when
// `qvr list` shows a TARGETS column while `default_target` reads as one
// value: callers treat the result as the canonical plural list.
func ParseDefaultTargets(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// Default returns a Config with default values.
func Default() *Config {
	return &Config{
		Registries:    make(map[string]RegistryConfig),
		DefaultTarget: "claude",
		Security: SecurityConfig{
			ScanOnInstall: true,
			BlockSeverity: "critical",
		},
		Output: OutputConfig{
			Format: "text",
			Color:  "auto",
		},
	}
}

// Load reads the config from disk. Returns defaults if file doesn't exist.
func Load() (*Config, error) {
	cfg := Default()
	data, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Registries == nil {
		cfg.Registries = make(map[string]RegistryConfig)
	}
	return cfg, nil
}

// Save writes the config to disk, creating the directory if needed.
func Save(cfg *Config) error {
	dir := Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(Path(), data, 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
