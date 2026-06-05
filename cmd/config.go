package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/quiver-cli/qvr/internal/config"
	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/output"
	"github.com/spf13/cobra"
)

// configValueValidators apply at `qvr config set` time so typos and bad
// values surface immediately, not later at install/scan time. Each returns
// the canonical/normalised value (or the input unchanged), and an error if
// the value is invalid for the key. Keys without a validator are accepted
// as-is.
var configValueValidators = map[string]func(string) (string, error){
	"default_target": func(v string) (string, error) {
		parts := strings.Split(v, ",")
		for i, p := range parts {
			parts[i] = strings.TrimSpace(p)
			if _, ok := model.Targets[parts[i]]; !ok {
				return "", fmt.Errorf("invalid agent target %q; valid: %s",
					parts[i], strings.Join(model.TargetNames(), ", "))
			}
		}
		return strings.Join(parts, ","), nil
	},
	"security.scan_on_install": validateBool,
	"security.require_scan":    validateBool,
	"security.require_signed":  validateBool,
	// Vocab must match what the scanner actually emits (internal/security
	// scanner.go: info|warning|error|critical). `security.scan_on_install
	// false` is the off switch — there's no "none" sentinel here on purpose.
	"security.block_severity": validateEnum([]string{
		"critical", "error", "warning", "info",
	}),
	"cache.index_ttl": validateDuration,
	"output.format":   validateEnum([]string{"text", "json"}),
	"output.color":    validateEnum([]string{"auto", "always", "never"}),
}

func validateBool(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "false":
		return strings.ToLower(strings.TrimSpace(v)), nil
	}
	return "", fmt.Errorf("must be true or false, got %q", v)
}

// validateDuration accepts any Go duration string the time package can parse
// ("1h", "15m", "0", "30s"). Negative durations are rejected. The validator
// re-formats the value to its canonical form so `qvr config get` shows the
// same string the user would type (e.g. "60m" → "1h0m0s"). The empty string
// is the documented "use the default" sentinel and is allowed through.
func validateDuration(v string) (string, error) {
	s := strings.TrimSpace(v)
	if s == "" {
		return "", nil
	}
	d, err := config.ParseCacheTTL(s)
	if err != nil {
		return "", err
	}
	return d.String(), nil
}

func validateEnum(valid []string) func(string) (string, error) {
	return func(v string) (string, error) {
		got := strings.ToLower(strings.TrimSpace(v))
		for _, ok := range valid {
			if got == ok {
				return got, nil
			}
		}
		sort.Strings(valid)
		return "", fmt.Errorf("invalid value %q; valid: %s", v, strings.Join(valid, ", "))
	}
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage quiver configuration",
}

var configGetCmd = &cobra.Command{
	Use:   "get [key]",
	Short: "Get a config value (or all values if no key)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runConfigGet,
}

var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a config value",
	Args:  cobra.ExactArgs(2),
	RunE:  runConfigSet,
}

func init() {
	configCmd.AddCommand(configGetCmd)
	configCmd.AddCommand(configSetCmd)
	rootCmd.AddCommand(configCmd)
}

// knownConfigKeys lists the user-facing dot-separated keys in the order
// `qvr config get` prints them. Kept here (not in internal/config) because
// the stringly-typed surface only exists for the CLI.
//
// Ops keys are surfaced because `ops.enabled` is the telemetry switch —
// pre-#124 it was hidden from `qvr config get` (text) but exposed in the
// JSON form, a privacy/trust footgun where users couldn't see what their
// install was doing without piping to jq.
var knownConfigKeys = []string{
	"default_target",
	"default_registry",
	"github_token",
	"security.scan_on_install",
	"security.require_scan",
	"security.require_signed",
	"security.block_severity",
	"output.format",
	"output.color",
	"cache.index_ttl",
	"ops.enabled",
	"ops.db_path",
}

// suggestSubKeys returns the known dotted keys nested under prefix
// (e.g. "security" -> ["security.scan_on_install", "security.require_scan", ...]).
// Empty when prefix is already dotted or doesn't match any section.
func suggestSubKeys(prefix string) []string {
	if prefix == "" || strings.Contains(prefix, ".") {
		return nil
	}
	p := prefix + "."
	var out []string
	for _, k := range knownConfigKeys {
		if strings.HasPrefix(k, p) {
			out = append(out, k)
		}
	}
	return out
}

// configRead returns the string form of key, or "" if unknown.
func configRead(cfg *config.Config, key string) string {
	switch key {
	case "default_target":
		return cfg.DefaultTarget
	case "default_registry":
		return cfg.DefaultRegistry
	case "github_token":
		return cfg.GithubToken
	case "security.scan_on_install":
		if cfg.Security.ScanOnInstall {
			return "true"
		}
		return "false"
	case "security.require_scan":
		if cfg.Security.RequireScan {
			return "true"
		}
		return "false"
	case "security.require_signed":
		if cfg.Security.RequireSigned {
			return "true"
		}
		return "false"
	case "security.block_severity":
		return cfg.Security.BlockSeverity
	case "output.format":
		return cfg.Output.Format
	case "output.color":
		return cfg.Output.Color
	case "cache.index_ttl":
		return cfg.Cache.IndexTTL
	case "ops.enabled":
		if cfg.Ops.Enabled {
			return "true"
		}
		return "false"
	case "ops.db_path":
		return cfg.Ops.DBPath
	}
	return ""
}

func configWrite(cfg *config.Config, key, value string) error {
	switch key {
	case "default_target":
		cfg.DefaultTarget = value
	case "default_registry":
		cfg.DefaultRegistry = value
	case "github_token":
		cfg.GithubToken = value
	case "security.scan_on_install":
		cfg.Security.ScanOnInstall = value == "true"
	case "security.require_scan":
		cfg.Security.RequireScan = value == "true"
	case "security.require_signed":
		cfg.Security.RequireSigned = value == "true"
	case "security.block_severity":
		cfg.Security.BlockSeverity = value
	case "output.format":
		cfg.Output.Format = value
	case "output.color":
		cfg.Output.Color = value
	case "cache.index_ttl":
		cfg.Cache.IndexTTL = value
	default:
		return fmt.Errorf("unknown config key: %s", key)
	}
	return nil
}

func runConfigGet(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if len(args) == 0 {
		if printer.Format == output.FormatJSON {
			return printer.JSON(cfg)
		}
		for _, k := range knownConfigKeys {
			v := configRead(cfg, k)
			if v != "" {
				fmt.Printf("%s = %s\n", k, v)
			}
		}
		// Registries are a map (name → URL) — not a dotted key but
		// load-bearing for "what is my install doing?", so render them
		// inline. Pre-#124 they only appeared in --output json, so
		// users had no way to see them from the text view.
		if len(cfg.Registries) > 0 {
			names := make([]string, 0, len(cfg.Registries))
			for n := range cfg.Registries {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				fmt.Printf("registries.%s = %s\n", n, cfg.Registries[n].URL)
			}
		}
		return nil
	}

	key := args[0]
	val := configRead(cfg, key)
	if val == "" {
		// Section names (e.g. "security", "output", "cache") have no
		// scalar value of their own. Tell the user that explicitly and
		// list the sub-keys they probably meant — beats the old
		// "unknown or empty config key" error, which lied when the user
		// was actually looking at a real (nested) section. OSS-readiness
		// finding.
		if children := suggestSubKeys(key); len(children) > 0 {
			return fmt.Errorf("%q is a section, not a value — try one of: %s",
				key, strings.Join(children, ", "))
		}
		return fmt.Errorf("unknown or empty config key: %s", key)
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(map[string]string{key: val})
	}
	fmt.Println(val)
	return nil
}

func runConfigSet(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	key, value := args[0], args[1]
	if validate, ok := configValueValidators[key]; ok {
		normalised, verr := validate(value)
		if verr != nil {
			return fmt.Errorf("invalid %s: %w", key, verr)
		}
		value = normalised
	}
	if err := configWrite(cfg, key, value); err != nil {
		return err
	}

	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(map[string]string{"key": key, "value": value})
	}
	printer.Success(fmt.Sprintf("Set %s = %s", key, value))
	return nil
}
