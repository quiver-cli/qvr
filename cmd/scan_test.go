package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/security"
)

// withScanPrinter replaces the package-level printer with one that
// captures stdout/stderr into buffers. Returns the buffers and a
// teardown that restores the previous printer.
func withScanPrinter(t *testing.T, format output.Format) (out, errBuf *bytes.Buffer, restore func()) {
	t.Helper()
	prev := printer
	out = &bytes.Buffer{}
	errBuf = &bytes.Buffer{}
	printer = &output.Printer{Out: out, Err: errBuf, Format: format}
	return out, errBuf, func() { printer = prev }
}

func resetScanFlags() {
	scanSeverity = "info"
	scanFailOn = "error"
}

func TestRunScanCleanSkillTextSucceeds(t *testing.T) {
	defer resetScanFlags()
	out, _, restore := withScanPrinter(t, output.FormatText)
	defer restore()

	dir := filepath.Join("..", "testdata", "clean-skill")
	if err := runScan(scanCmd, []string{dir}); err != nil {
		t.Fatalf("expected nil error on clean skill, got %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "scan clean") {
		t.Errorf("expected success line in output, got %q", got)
	}
}

func TestRunScanCleanSkillJSONReportsZeroFindings(t *testing.T) {
	defer resetScanFlags()
	out, _, restore := withScanPrinter(t, output.FormatJSON)
	defer restore()

	dir := filepath.Join("..", "testdata", "clean-skill")
	if err := runScan(scanCmd, []string{dir}); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	var res security.ScanResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("decode JSON: %v\nbody: %s", err, out.String())
	}
	if len(res.Findings) != 0 {
		t.Errorf("expected zero findings on clean skill, got %v", res.Findings)
	}
	if res.Summary.Total() != 0 {
		t.Errorf("expected zero summary total, got %+v", res.Summary)
	}
	if res.Skill != "clean-skill" {
		t.Errorf("expected skill name in result, got %q", res.Skill)
	}
}

func TestRunScanSecretsFixtureCriticalsExitOne(t *testing.T) {
	defer resetScanFlags()
	out, _, restore := withScanPrinter(t, output.FormatText)
	defer restore()

	dir := filepath.Join("..", "testdata", "malicious-skill-secrets")
	err := runScan(scanCmd, []string{dir})
	if err == nil {
		t.Fatal("expected non-nil error on secrets fixture")
	}
	if !strings.Contains(err.Error(), "scan found") {
		t.Errorf("expected scan-found error, got %v", err)
	}
	if !strings.Contains(out.String(), "critical") {
		t.Errorf("expected critical in table, got %q", out.String())
	}
}

func TestRunScanInjectionFixtureWarningsBelowDefaultFailOn(t *testing.T) {
	defer resetScanFlags()
	// Injection-only findings sit at warning severity (the rule
	// taxonomy reserves error/critical for actionable exfil or system
	// prompt leakage that the fixture line-wraps across). Default
	// fail-on=error therefore exits 0.
	_, _, restore := withScanPrinter(t, output.FormatText)
	defer restore()

	dir := filepath.Join("..", "testdata", "malicious-skill-injection")
	if err := runScan(scanCmd, []string{dir}); err != nil {
		t.Fatalf("default fail-on=error should not trip on warnings: %v", err)
	}
}

func TestRunScanInjectionFixtureFailOnWarning(t *testing.T) {
	defer resetScanFlags()
	scanFailOn = "warning"
	_, _, restore := withScanPrinter(t, output.FormatText)
	defer restore()

	dir := filepath.Join("..", "testdata", "malicious-skill-injection")
	if err := runScan(scanCmd, []string{dir}); err == nil {
		t.Fatal("expected non-nil error when fail-on=warning and warnings exist")
	}
}

func TestRunScanJSONFailureSurfacesViaSentinel(t *testing.T) {
	defer resetScanFlags()
	out, _, restore := withScanPrinter(t, output.FormatJSON)
	defer restore()

	dir := filepath.Join("..", "testdata", "malicious-skill-secrets")
	err := runScan(scanCmd, []string{dir})
	if !errors.Is(err, errJSONHandled) {
		t.Fatalf("expected errJSONHandled, got %v", err)
	}

	var res security.ScanResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("decode JSON: %v\nbody: %s", err, out.String())
	}
	if res.Summary.Critical == 0 {
		t.Errorf("expected critical findings in summary, got %+v", res.Summary)
	}
	if len(res.Findings) == 0 {
		t.Error("expected findings to be present")
	}
	hasSecretsCheck := false
	for _, f := range res.Findings {
		if f.Check == security.SecretsCheckName {
			hasSecretsCheck = true
			break
		}
	}
	if !hasSecretsCheck {
		t.Errorf("expected at least one secrets finding")
	}
}

// TestRunScanStructuredFormatKeepsStdoutPure is the regression guard
// for issue #35: --format json/sarif/markdown must not mix the
// "discovered skill at ..." progress line into stdout, because that
// breaks jq / SARIF viewers / `gh code-scanning upload`.
func TestRunScanStructuredFormatKeepsStdoutPure(t *testing.T) {
	defer resetScanFlags()
	for _, format := range []string{"json", "sarif", "markdown"} {
		t.Run(format, func(t *testing.T) {
			scanFormat = format
			defer func() { scanFormat = "" }()
			out, errBuf, restore := withScanPrinter(t, output.FormatText)
			defer restore()

			// sample-registry is a multi-skill layout, so the resolver
			// will discover the inner skill and print the progress
			// line — exactly the case that used to corrupt JSON.
			dir := filepath.Join("..", "testdata", "sample-registry")
			_ = runScan(scanCmd, []string{dir})
			stdout := out.String()
			stderr := errBuf.String()
			if strings.Contains(stdout, "discovered skill at") {
				t.Errorf("--format %s leaked progress line to stdout:\n%s", format, stdout)
			}
			if !strings.Contains(stderr, "discovered skill at") {
				t.Errorf("expected progress line on stderr, got %q", stderr)
			}
		})
	}
}

// TestRunScanInvalidFormatFlag is the regression guard for issue #36:
// `--format pirate` used to silently fall back to text. It must now
// reject with a friendly error matching the --severity convention.
func TestRunScanInvalidFormatFlag(t *testing.T) {
	defer resetScanFlags()
	scanFormat = "pirate"
	defer func() { scanFormat = "" }()
	_, _, restore := withScanPrinter(t, output.FormatText)
	defer restore()

	err := runScan(scanCmd, []string{filepath.Join("..", "testdata", "clean-skill")})
	if err == nil {
		t.Fatal("expected error from --format pirate")
	}
	if !strings.Contains(err.Error(), "--format:") {
		t.Errorf("expected --format error, got %v", err)
	}
}

func TestRunScanInvalidSeverityFlag(t *testing.T) {
	defer resetScanFlags()
	scanSeverity = "fatal"
	_, _, restore := withScanPrinter(t, output.FormatText)
	defer restore()

	err := runScan(scanCmd, []string{filepath.Join("..", "testdata", "clean-skill")})
	if err == nil || !strings.Contains(err.Error(), "--severity") {
		t.Fatalf("expected --severity error, got %v", err)
	}
}

func TestRunScanSeverityFilterHidesLowerFindings(t *testing.T) {
	defer resetScanFlags()
	scanSeverity = "critical"
	out, _, restore := withScanPrinter(t, output.FormatJSON)
	defer restore()

	dir := filepath.Join("..", "testdata", "malicious-skill-injection")
	// injection findings are warnings; severity=critical filters them out.
	if err := runScan(scanCmd, []string{dir}); err != nil {
		t.Fatalf("default fail-on=error should not trip when nothing is critical: %v", err)
	}
	var res security.ScanResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Errorf("expected no findings after severity=critical filter, got %v", res.Findings)
	}
	// Summary should STILL reflect the unfiltered scan.
	if res.Summary.Warning == 0 {
		t.Errorf("summary should retain unfiltered counts, got %+v", res.Summary)
	}
}
