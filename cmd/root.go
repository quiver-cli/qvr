// Package cmd implements the qvr Cobra command tree: one file per command,
// all registered on rootCmd, with a persistent --output flag that routes
// structured data to stdout and diagnostics to stderr.
package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/astra-sh/qvr/internal/output"
	"github.com/spf13/cobra"
)

var (
	outputFormat string
	printer      *output.Printer

	// Build provenance — overridden at link time via -ldflags (see Makefile and
	// .goreleaser.yaml). The defaults make an un-stamped build (plain `go build`
	// or `go run` with no ldflags) self-identify as a dev build rather than
	// impersonate a real release. `make build`/`make run` and goreleaser all
	// stamp these from git, so any installed binary reports exactly what it is.
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// errJSONHandled signals to Execute() that the command has already emitted a
// structured JSON payload encoding its failure, and the top-level envelope
// should be suppressed. Exit code is still 1 — only the second JSON document
// (the "{\"error\": \"...\"}" envelope) is skipped. Use this in JSON paths
// where the body already carries "valid": false / "failed": N / etc.
var errJSONHandled = errors.New("json payload already emitted")

// errTextHandled is the text-mode equivalent of errJSONHandled — the command
// already printed a per-skill `error: ...: <reason>` line for every failure,
// so the top-level `error:` envelope would just duplicate the first one.
// Return this from RunE when the command surfaced failures itself (e.g. batch
// `qvr add` where two skills failed and one succeeded). Exit code stays 1.
// Issue #66 — without this, partial-failure batches read like total failures
// because the trailing duplicate `error:` is the last line on stderr.
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

// rejectUnknownSubcommand is the RunE for a pure parent command (one with no
// standalone behavior of its own). With no positional args it prints help; a
// positional means a typo'd subcommand (e.g. `qvr registry ad`), which must
// exit non-zero instead of silently printing help with exit 0 — otherwise a
// CI/script reads the typo as success while nothing ran (issues #120, #169).
func rejectUnknownSubcommand(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath())
	}
	return cmd.Help()
}

// Execute runs the root command and is the binary's sole entry point from
// main. On failure it prints the error (a structured JSON envelope when
// --output json, a plain `error:` line otherwise) and exits 1; commands that
// already surfaced their own failure output signal that via errJSONHandled /
// errTextHandled so the envelope is not duplicated.
func Execute() {
	assignCommandGroups(rootCmd)
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
		fmt.Fprintf(os.Stderr, "%s %s\n", output.NewStyler(os.Stderr).BoldRed("error:"), err.Error())
	}
	os.Exit(1)
}

func init() {
	rootCmd.PersistentFlags().StringVar(&outputFormat, "output", "text", "output format (text|json)")
	rootCmd.SetVersionTemplate("qvr version {{.Version}}\n")
	// Force-materialise cobra's auto-generated `completion` parent command
	// so we can override its RunE. Without this, `qvr completion <garbage>`
	// falls through to the default that prints help with exit 0 — a CI
	// script doing `qvr completion "$SHELL_KIND"` with $SHELL_KIND unset
	// silently installs an empty completion file. Issue #120.
	rootCmd.InitDefaultCompletionCmd()
	for _, c := range rootCmd.Commands() {
		if c.Name() == "completion" {
			c.RunE = rejectUnknownSubcommand
			break
		}
	}
}
