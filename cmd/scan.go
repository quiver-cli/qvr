package cmd

import (
	"context"
	"fmt"
	"strconv"

	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/security"
	"github.com/raks097/quiver/internal/skill"
	"github.com/spf13/cobra"
)

var (
	scanSeverity     string
	scanFailOn       string
	scanGlobal       bool
	scanFormat       string
	scanMaxFileBytes int64 = -1 // -1 = no override; use security default
)

var scanCmd = &cobra.Command{
	Use:   "scan [path]",
	Short: "Run security checks against a skill",
	Long: `Scan a skill directory for prompt-injection patterns, leaked
credentials, hidden unicode, and risky permissions. The scanner never
executes anything — every file is read as a string and the executable
bit is reported, not honoured.

The exit code reflects --fail-on (default: error). A clean scan exits 0;
a scan that produces a finding at or above --fail-on exits 1.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runScan,
}

func init() {
	scanCmd.Flags().StringVar(&scanSeverity, "severity", "info",
		"only show findings at or above this severity (info|warning|error|critical)")
	scanCmd.Flags().StringVar(&scanFailOn, "fail-on", "error",
		"exit non-zero when any finding meets or exceeds this severity")
	scanCmd.Flags().BoolVar(&scanGlobal, "global", false,
		"when resolving a positional as an installed skill name, read the user-global lock")
	scanCmd.Flags().StringVar(&scanFormat, "format", "",
		"report format override (text|json|sarif|markdown); takes precedence over --output")
	scanCmd.Flags().Int64Var(&scanMaxFileBytes, "max-file-bytes", -1,
		"per-file content cap in bytes (0 disables the cap); overrides QVR_MAX_FILE_BYTES and the 10 MiB default")
	rootCmd.AddCommand(scanCmd)
}

func runScan(cmd *cobra.Command, args []string) error {
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}

	severityMin, err := security.ParseSeverity(scanSeverity)
	if err != nil {
		return fmt.Errorf("--severity: %w", err)
	}
	failOn, err := security.ParseSeverity(scanFailOn)
	if err != nil {
		return fmt.Errorf("--fail-on: %w", err)
	}

	// Validate --format up front so typos fail loudly (issue #36).
	// An empty string is the documented "fall back to --output" case
	// and is treated as valid.
	switch scanFormat {
	case "", "text", "json", "sarif", "markdown":
		// ok
	default:
		return fmt.Errorf("--format: invalid value %q: expected one of text, json, sarif, markdown", scanFormat)
	}

	// `--max-file-bytes` overrides QVR_MAX_FILE_BYTES (which security
	// init() already applied). -1 is the sentinel "no flag given" so
	// the env / default wins; 0 disables the cap entirely.
	if scanMaxFileBytes >= 0 {
		prev := security.SetMaxScanBytes(scanMaxFileBytes)
		defer security.SetMaxScanBytes(prev)
	}

	// External inputs (git URL, zip archive, single SKILL.md) get
	// materialised into a local directory before the standard
	// resolveScanTarget path runs. Cleanup is deferred so a clone /
	// extraction temp dir doesn't outlive the command.
	if external, err := maybeResolveExternalInput(dir); err != nil {
		return err
	} else if external != "" {
		dir = external
		if scanInputCleanup != nil {
			defer func() {
				_ = scanInputCleanup()
				scanInputCleanup = nil
			}()
		}
	}

	resolved, discovered, err := resolveSkillArg(dir, scanGlobal)
	if err != nil {
		if printer.Format == output.FormatJSON {
			_ = printer.JSON(map[string]any{
				"path":       dir,
				"discovered": discovered,
				"error":      err.Error(),
			})
			return errJSONHandled
		}
		return err
	}
	if resolved != dir {
		// stderr only — stdout is reserved for the structured payload
		// when --format json/sarif/markdown is in use (issue #35).
		// In text mode, stderr still surfaces it to the human.
		fmt.Fprintf(printer.Err, "discovered skill at %s\n", resolved)
	}

	s, err := skill.LoadFromPath(resolved)
	if err != nil {
		if printer.Format == output.FormatJSON {
			_ = printer.JSON(map[string]any{
				"path":  resolved,
				"error": err.Error(),
			})
			return errJSONHandled
		}
		return fmt.Errorf("load skill: %w", err)
	}

	scanner := security.New()
	if p := security.LLMProviderFromEnv(); p != nil {
		scanner = scanner.WithLLMProvider(p)
		for _, lc := range security.BuiltinLLMChecks() {
			scanner = scanner.AddLLM(lc)
		}
	}
	result, err := scanner.Scan(context.Background(), s, resolved)
	if err != nil {
		return fmt.Errorf("scan: %w", err)
	}

	// Apply the display filter without rewriting Summary — the summary
	// always reflects the *complete* finding set so downstream tooling
	// doesn't have to recompute it.
	result.Findings = security.Filter(result.Findings, severityMin)

	switch effectiveScanFormat(printer.Format) {
	case "sarif":
		if err := printer.JSON(security.ToSARIF(result)); err != nil {
			return err
		}
		if exceedsThreshold(result, failOn) {
			return errJSONHandled
		}
		return nil
	case "markdown":
		fmt.Fprint(printer.Out, security.ToMarkdown(result))
		if exceedsThreshold(result, failOn) {
			return fmt.Errorf("scan found %d finding(s) at or above %s",
				countAbove(result.Summary, failOn), failOn)
		}
		return nil
	case "json":
		if err := printer.JSON(result); err != nil {
			return err
		}
		if exceedsThreshold(result, failOn) {
			return errJSONHandled
		}
		return nil
	}

	renderScanText(result, failOn)
	if exceedsThreshold(result, failOn) {
		return fmt.Errorf("scan found %d finding(s) at or above %s",
			countAbove(result.Summary, failOn), failOn)
	}
	return nil
}

// effectiveScanFormat honors `--format` when set, otherwise falls back
// to the global `--output` printer format. The string return makes the
// switch above easier to read than juggling typed enums.
func effectiveScanFormat(global output.Format) string {
	if scanFormat != "" {
		return scanFormat
	}
	if global == output.FormatJSON {
		return "json"
	}
	return "text"
}

// renderScanText prints the human-friendly table + summary footer.
// Findings is already display-filtered at this point; Summary counts
// the unfiltered set so the footer reflects the truth.
func renderScanText(result *security.ScanResult, failOn security.Severity) {
	if len(result.Findings) == 0 {
		printer.Success(fmt.Sprintf("scan clean for %s (%d check(s) ran)", result.Skill, len(result.Checks)))
		return
	}

	rows := make([][]string, 0, len(result.Findings))
	for _, f := range result.Findings {
		loc := f.File
		if f.Line > 0 {
			loc = loc + ":" + strconv.Itoa(f.Line)
		}
		rows = append(rows, []string{string(f.Severity), f.Check, loc, f.Message})
	}
	printer.Table([]string{"SEVERITY", "CHECK", "LOCATION", "MESSAGE"}, rows)

	fmt.Fprintf(printer.Out, "\nsummary: %d critical, %d error, %d warning, %d info — fail-on=%s\n",
		result.Summary.Critical, result.Summary.Error, result.Summary.Warning, result.Summary.Info, failOn)
}

func exceedsThreshold(result *security.ScanResult, threshold security.Severity) bool {
	return countAbove(result.Summary, threshold) > 0
}

// Argument resolution (resolveScanTarget, resolveByLock, looksLikePath)
// moved to cmd/resolve.go in v0.6.1 so qvr validate can share it (issue
// #64). qvr scan calls resolveSkillArg(dir, scanGlobal) directly above.

func countAbove(s security.Summary, threshold security.Severity) int {
	cutoff := threshold.Rank()
	if cutoff < 0 {
		return 0
	}
	count := 0
	if security.SeverityCritical.Rank() >= cutoff {
		count += s.Critical
	}
	if security.SeverityError.Rank() >= cutoff {
		count += s.Error
	}
	if security.SeverityWarning.Rank() >= cutoff {
		count += s.Warning
	}
	if security.SeverityInfo.Rank() >= cutoff {
		count += s.Info
	}
	return count
}
