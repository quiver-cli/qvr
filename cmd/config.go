package cmd

import (
	"fmt"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/output"
	"github.com/spf13/cobra"
)

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
var knownConfigKeys = []string{
	"default_target",
	"default_registry",
	"github_token",
	"security.scan_on_install",
	"security.block_severity",
	"output.format",
	"output.color",
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
	case "security.block_severity":
		return cfg.Security.BlockSeverity
	case "output.format":
		return cfg.Output.Format
	case "output.color":
		return cfg.Output.Color
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
	case "security.block_severity":
		cfg.Security.BlockSeverity = value
	case "output.format":
		cfg.Output.Format = value
	case "output.color":
		cfg.Output.Color = value
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
		return nil
	}

	key := args[0]
	val := configRead(cfg, key)
	if val == "" {
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
