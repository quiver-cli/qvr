package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/raks097/quiver/internal/output"
	"github.com/spf13/cobra"
)

var (
	outputFormat string
	printer      *output.Printer
	version      = "0.4.9"
)

// errJSONHandled signals to Execute() that the command has already emitted a
// structured JSON payload encoding its failure, and the top-level envelope
// should be suppressed. Exit code is still 1 — only the second JSON document
// (the "{\"error\": \"...\"}" envelope) is skipped. Use this in JSON paths
// where the body already carries "valid": false / "failed": N / etc.
var errJSONHandled = errors.New("json payload already emitted")

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
	// errJSONHandled means the command already emitted a structured JSON payload
	// that encodes the failure (e.g. {"valid": false} or {"failed": 1}). Suppress
	// the duplicate top-level envelope so the stream stays a single JSON doc.
	if errors.Is(err, errJSONHandled) {
		os.Exit(1)
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
