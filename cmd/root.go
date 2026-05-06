package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/raks097/quiver/internal/output"
	"github.com/spf13/cobra"
)

var (
	outputFormat string
	printer      *output.Printer
	version      = "0.4.4"
)

var rootCmd = &cobra.Command{
	Use:   "qvr",
	Short: "Quiver — agent skills manager",
	Long:  `Quiver is a CLI-native agent skills manager. Git repos as registries, symlinks as installs.`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		format := output.FormatText
		if outputFormat == "json" {
			format = output.FormatJSON
		}
		printer = output.New(format)
	},
	Version: version,
	// Runtime errors from RunE shouldn't trigger a usage/help dump — cobra's
	// default is to print Error + full usage on any non-nil return. We handle
	// printing in Execute() so --output json can emit a structured envelope.
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute() {
	err := rootCmd.Execute()
	if err == nil {
		return
	}
	if outputFormat == "json" {
		enc := json.NewEncoder(os.Stderr)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]string{"error": err.Error()})
	} else {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
	}
	os.Exit(1)
}

func init() {
	rootCmd.PersistentFlags().StringVar(&outputFormat, "output", "text", "output format (text|json)")
	rootCmd.SetVersionTemplate("qvr version {{.Version}}\n")
}
