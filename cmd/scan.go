package cmd

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/quiver-cli/qvr/internal/output"
	"github.com/quiver-cli/qvr/internal/security"
	"github.com/quiver-cli/qvr/internal/skill"
	"github.com/spf13/cobra"
)

var (
	scanSeverity     string
	scanFailOn       string
	scanGlobal       bool
	scanFormat       string
	scanMaxFileBytes int64 = -1 // -1 = no override; use security default
	scanAgainst      string
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
	scanCmd.Flags().StringVar(&scanAgainst, "against", "",
		"only report findings new relative to this Git ref")
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
	if scanAgainst != "" {
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		baseline, cleanup, err := scanBaseline(ctx, scanner, resolved, scanAgainst)
		if cleanup != nil {
			defer cleanup()
		}
		if err != nil {
			return err
		}
		result.Findings = diffFindings(result.Findings, baseline.Findings)
		result.Summary = summariseScanFindings(result.Findings)
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

func scanBaseline(ctx context.Context, scanner *security.Scanner, resolved, ref string) (*security.ScanResult, func(), error) {
	dir, cleanup, err := materializeGitRefPath(ctx, resolved, ref)
	if err != nil {
		return nil, cleanup, err
	}
	s, err := skill.LoadFromPath(dir)
	if err != nil {
		return nil, cleanup, fmt.Errorf("--against %s: baseline does not contain a loadable skill at the same path: %w", ref, err)
	}
	res, err := scanner.Scan(ctx, s, dir)
	if err != nil {
		return nil, cleanup, fmt.Errorf("--against %s: scan baseline: %w", ref, err)
	}
	return res, cleanup, nil
}

func materializeGitRefPath(ctx context.Context, resolved, ref string) (string, func(), error) {
	resolvedAbs, err := filepath.Abs(resolved)
	if err != nil {
		return "", nil, fmt.Errorf("--against: resolve absolute path: %w", err)
	}
	if realResolved, err := filepath.EvalSymlinks(resolvedAbs); err == nil {
		resolvedAbs = realResolved
	}
	rootBytes, err := exec.CommandContext(ctx, "git", "-C", resolved, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", nil, fmt.Errorf("--against requires %s to be inside a Git worktree", resolved)
	}
	root := strings.TrimSpace(string(rootBytes))
	if realRoot, err := filepath.EvalSymlinks(root); err == nil {
		root = realRoot
	}
	rel, err := filepath.Rel(root, resolvedAbs)
	if err != nil {
		return "", nil, fmt.Errorf("--against: resolve path relative to git root: %w", err)
	}
	if rel == "" {
		rel = "."
	}
	archivePath := filepath.ToSlash(rel)
	if archivePath == "." {
		archivePath = "."
	}
	cmd := exec.CommandContext(ctx, "git", "-C", root, "archive", ref, "--", archivePath)
	data, err := cmd.Output()
	if err != nil {
		return "", nil, fmt.Errorf("--against %s: git archive failed for %s", ref, archivePath)
	}
	tmp, err := os.MkdirTemp("", "qvr-scan-against-*")
	if err != nil {
		return "", nil, fmt.Errorf("--against: create temp dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	if err := extractTar(data, tmp); err != nil {
		cleanup()
		return "", nil, err
	}
	baselineDir := tmp
	if rel != "." {
		baselineDir = filepath.Join(tmp, rel)
	}
	return baselineDir, cleanup, nil
}

func extractTar(data []byte, dest string) error {
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("--against: read archive: %w", err)
		}
		name := filepath.Clean(h.Name)
		if name == "." {
			continue
		}
		target := filepath.Join(dest, name)
		if !strings.HasPrefix(target, dest+string(os.PathSeparator)) {
			return fmt.Errorf("--against: archive entry escapes destination: %s", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("--against: mkdir %s: %w", name, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("--against: mkdir parent %s: %w", name, err)
			}
			mode := os.FileMode(h.Mode) & 0o777
			if mode == 0 {
				mode = 0o644
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return fmt.Errorf("--against: create %s: %w", name, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				_ = f.Close()
				return fmt.Errorf("--against: extract %s: %w", name, err)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("--against: close %s: %w", name, err)
			}
		}
	}
}

func diffFindings(current, baseline []security.Finding) []security.Finding {
	seen := make(map[string]struct{}, len(baseline))
	for _, f := range baseline {
		seen[findingKey(f)] = struct{}{}
	}
	out := make([]security.Finding, 0, len(current))
	for _, f := range current {
		if _, ok := seen[findingKey(f)]; !ok {
			out = append(out, f)
		}
	}
	return out
}

func findingKey(f security.Finding) string {
	return strings.Join([]string{
		string(f.Severity),
		f.Check,
		f.RuleID,
		f.File,
		strconv.Itoa(f.Line),
		f.Message,
	}, "\x00")
}

func summariseScanFindings(findings []security.Finding) security.Summary {
	var s security.Summary
	for _, f := range findings {
		switch f.Severity {
		case security.SeverityCritical:
			s.Critical++
		case security.SeverityError:
			s.Error++
		case security.SeverityWarning:
			s.Warning++
		case security.SeverityInfo:
			s.Info++
		}
	}
	return s
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
