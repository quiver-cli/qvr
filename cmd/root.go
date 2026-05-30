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
	version      = "0.8.9"
)

// errJSONHandled signals to Execute() that the command has already emitted a
// structured JSON payload encoding its failure, and the top-level envelope
// should be suppressed. Exit code is still 1 — only the second JSON document
// (the "{\"error\": \"...\"}" envelope) is skipped. Use this in JSON paths
// where the body already carries "valid": false / "failed": N / etc.
var errJSONHandled = errors.New("json payload already emitted")

// errTextHandled is the text-mode equivalent of errJSONHandled — the command
// already printed a per-skill `✗ ...: <reason>` line for every failure, so the
// top-level `Error: ...` envelope would just duplicate the first one. Return
// this from RunE when the command surfaced failures itself (e.g. batch
// `qvr add` where two skills failed and one succeeded). Exit code stays 1.
// Issue #66 — without this, partial-failure batches read like total failures
// because the trailing duplicate `Error:` is the last line on stderr.
var errTextHandled = errors.New("text already emitted failure")

var rootCmd = &cobra.Command{
	Use:   "qvr",
	Short: "Quiver — agent skills manager",
	Long:  `Quiver is a CLI-native agent skills manager. Git repos as registries, symlinks as installs.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Validate the persistent --output flag so typos like
		// `--output jsno` fail loudly instead of falling back to
		// text and silently breaking downstream pipes (issue #36).
		switch outputFormat {
		case "text", "json":
			// ok
		default:
			return fmt.Errorf("--output: invalid value %q: expected one of text, json", outputFormat)
		}
		format := output.FormatText
		if outputFormat == "json" {
			format = output.FormatJSON
		}
		printer = output.New(format)
		return nil
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
	if errors.Is(err, errJSONHandled) || errors.Is(err, errTextHandled) {
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
