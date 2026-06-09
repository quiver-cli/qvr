package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultCacheTTL is the freshness window applied when cache.index_ttl is
// unset or unparseable. One hour matches the historical hardcoded value
// (registry.DefaultCacheTTL) so users who never touch the config see the
// same behaviour they did before #46.
const DefaultCacheTTL = time.Hour

// ParseCacheTTL turns a CacheConfig.IndexTTL string into a duration. An
// empty string returns DefaultCacheTTL. A negative duration is rejected.
// "0" is accepted and means "always rebuild on next read" — the IsStale
// check at the registry layer treats any Generated timestamp older than
// zero seconds as stale.
func ParseCacheTTL(raw string) (time.Duration, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return DefaultCacheTTL, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", raw, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("duration must be non-negative, got %s", d)
	}
	return d, nil
}

// Config represents the quiver configuration.
type Config struct {
	Registries      map[string]RegistryConfig `yaml:"registries,omitempty" json:"registries,omitempty"`
	DefaultTarget   string                    `yaml:"default_target,omitempty" json:"default_target,omitempty"`
	DefaultRegistry string                    `yaml:"default_registry,omitempty" json:"default_registry,omitempty"`
	GithubToken     string                    `yaml:"github_token,omitempty" json:"github_token,omitempty"`
	Security        SecurityConfig            `yaml:"security,omitempty" json:"security"`
	Trust           TrustConfig               `yaml:"trust,omitempty" json:"trust"`
	Output          OutputConfig              `yaml:"output,omitempty" json:"output"`
	Cache           CacheConfig               `yaml:"cache,omitempty" json:"cache"`
	Ops             OpsConfig                 `yaml:"ops,omitempty" json:"ops"`
	Prefetch        PrefetchConfig            `yaml:"prefetch,omitempty" json:"prefetch"`
}

// PrefetchConfig controls opportunistic background prefetch of registered
// registries (#211). It is OFF by default: with Enabled false, qvr never spawns
// a background refresh, so existing users see no behaviour change. When enabled,
// MinInterval throttles how often a prefetch may run (a Go duration string like
// "30m"; empty means the package default). Prefetch is always best-effort — it
// can never block or fail a foreground command.
type PrefetchConfig struct {
	Enabled     bool   `yaml:"enabled" json:"enabled"`
	MinInterval string `yaml:"min_interval,omitempty" json:"min_interval,omitempty"`
}

// TrustConfig stores optional registry-level author policy. It is separate
// from SecurityConfig because it names human/org policy, not scanner gates.
type TrustConfig struct {
	Registries map[string]RegistryTrustConfig `yaml:"registries,omitempty" json:"registries,omitempty"`
}

// RegistryTrustConfig is the trust policy for one registry.
type RegistryTrustConfig struct {
	Authors []string `yaml:"authors,omitempty" json:"authors,omitempty"`
	Signers []string `yaml:"signers,omitempty" json:"signers,omitempty"`
}

// CacheConfig controls the registry-index cache freshness window. IndexTTL
// is stored as the duration string the user wrote (e.g. "1h", "15m", "0")
// so on-disk YAML round-trips unchanged. Parse with ParseCacheTTL when
// you need a time.Duration for IsStale.
type CacheConfig struct {
	IndexTTL string `yaml:"index_ttl,omitempty" json:"index_ttl,omitempty"`
}

// OpsConfig controls the SkillOps audit pipeline. The struct lives here
// (not in internal/ops) so that internal/ops can depend on internal/config
// without a cycle. In the raw-only model the only knobs are the on/off switch
// and the database location; capture stores verbatim, so there is no per-agent
// logging level, retention, or skill-less policy to configure.
type OpsConfig struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	DBPath  string `yaml:"db_path,omitempty" json:"db_path,omitempty"`
}

// RegistryConfig holds configuration for a single registry.
type RegistryConfig struct {
	URL string `yaml:"url" json:"url"`
}

// SecurityConfig holds security-related settings.
type SecurityConfig struct {
	ScanOnInstall bool   `yaml:"scan_on_install" json:"scan_on_install"`
	RequireScan   bool   `yaml:"require_scan,omitempty" json:"require_scan,omitempty"`
	RequireSigned bool   `yaml:"require_signed,omitempty" json:"require_signed,omitempty"`
	BlockSeverity string `yaml:"block_severity,omitempty" json:"block_severity,omitempty"`
}

// OutputConfig holds output preferences.
type OutputConfig struct {
	Format string `yaml:"format,omitempty" json:"format,omitempty"`
	Color  string `yaml:"color,omitempty" json:"color,omitempty"`
}

// Dir returns the quiver home directory. Override with QVR_HOME (preferred,
// matches the binary name and the QVR_* prefix used by every other env var —
// QVR_MAX_FILE_BYTES, QVR_LLM_PROVIDER, …) or QUIVER_HOME (legacy alias kept
// for back-compat). QVR_HOME wins if both are set. Issue #119.
func Dir() string {
	if env := os.Getenv("QVR_HOME"); env != "" {
		return env
	}
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
