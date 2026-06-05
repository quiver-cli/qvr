// Package security implements the deterministic skill scanner.
//
// The scanner is a pipeline of independent [Check] implementations
// (prompt-injection patterns, credential scanning, hidden unicode,
// permission analysis) that produce [Finding]s with a structured
// severity ladder. Nothing in the package executes the contents of a
// skill — every file is read as a string, and files with the executable
// bit set are flagged, not run.
//
// The pipeline is intentionally synchronous and deterministic so the
// JSON output of `qvr scan` is a stable contract the upcoming
// `qvr review` command and skill-card renderer can consume. The Run
// method takes a [context.Context] so a future asynchronous check (for
// example an LLM-based detector) can plug in as a sibling without
// changing the interface.
package security

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/raks097/quiver/internal/model"
)

// Severity is the four-step ladder used for findings.
//
// info < warning < error < critical, by [Severity.Rank].
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityError    Severity = "error"
	SeverityCritical Severity = "critical"
)

// Rank returns the numeric ordering of a severity. Unknown severities
// rank as -1 so they never satisfy a `--fail-on` threshold by accident.
func (s Severity) Rank() int {
	switch s {
	case SeverityInfo:
		return 0
	case SeverityWarning:
		return 1
	case SeverityError:
		return 2
	case SeverityCritical:
		return 3
	default:
		return -1
	}
}

// ParseSeverity returns the canonical Severity for an input string, or
// an error listing valid values. Used by the CLI to validate
// `--severity` and `--fail-on` flags.
func ParseSeverity(s string) (Severity, error) {
	switch Severity(s) {
	case SeverityInfo, SeverityWarning, SeverityError, SeverityCritical:
		return Severity(s), nil
	}
	return "", fmt.Errorf("invalid severity %q: expected one of info, warning, error, critical", s)
}

// Finding is one observation reported by a [Check].
//
// File is relative to the skill directory and uses forward slashes for
// JSON portability. Line is 1-indexed; 0 means "no line attribution"
// (for example, a finding about the frontmatter as a whole, or about a
// file's mode bits).
//
// RuleID, Category, and Confidence are populated by the unified rule
// engine (see rules.go); checks that don't use the engine leave them
// empty. The JSON omits empty values so the historical shape of
// findings — emitted before these fields existed — is unchanged.
type Finding struct {
	Check      string   `json:"check"`
	RuleID     string   `json:"rule_id,omitempty"`
	Category   Category `json:"category,omitempty"`
	Severity   Severity `json:"severity"`
	Confidence float64  `json:"confidence,omitempty"`
	File       string   `json:"file,omitempty"`
	Line       int      `json:"line,omitempty"`
	// Evidence is the offending source line (whitespace-trimmed, rune-capped)
	// that triggered the match, so a reader can see *what* fired without
	// re-opening the file at File:Line. Empty for findings with no single-line
	// attribution (frontmatter-wide rules, mode-bit checks). Populated by the
	// rule engine; checks that don't use it leave it empty.
	Evidence    string `json:"evidence,omitempty"`
	Message     string `json:"message"`
	Remediation string `json:"remediation,omitempty"`
}

// Check is the contract for a single scanner stage.
//
// A check inspects the loaded skill and its files in memory and returns
// zero or more findings. The context is the seam for future async or
// long-running checks (LLM-based detection); deterministic checks may
// ignore it.
type Check interface {
	Name() string
	Run(ctx context.Context, skill *model.Skill, files []FileEntry) []Finding
}

// Scanner runs a configured pipeline of checks. The zero value is not
// usable — construct with [New] or [NewWithChecks].
//
// llmChecks holds optional [LLMCheck]s that run in the same pass when
// an [LLMProvider] is configured. They no-op (returning nil) when
// provider is nil so `qvr scan` remains fully usable offline.
type Scanner struct {
	checks    []Check
	llmChecks []LLMCheck
	provider  LLMProvider
}

// New returns a scanner with the built-in deterministic checks
// registered. Order is stable so JSON output diffs cleanly between
// runs.
//
// The current set:
//
//   - prompt_injection — instruction-override / system-prompt-leakage
//     patterns (rule IDs P1-P10)
//   - secrets — high-precision credential prefixes (AWS, GitHub, JWT, …)
//   - unicode — zero-width / bidi-override / tag chars + homoglyphs
//   - permissions — allowed-tools, executable bit, dangerous shell
//   - patterns — every other rule in the detection registry:
//     data exfiltration, privilege escalation, supply chain (pattern
//     subset), excessive agency, output handling, memory poisoning,
//     tool misuse, rogue agent, harmful content, trigger abuse
//   - mcp_tool_poisoning — hidden instructions / unicode / encoded
//     blobs inside SKILL.md frontmatter fields
//   - mcp_least_privilege — declared allowed-tools / permissions vs
//     capabilities actually used in the skill's code
//   - signatures — YARA-lite signature matches across malware,
//     webshells, cryptominers, and offensive-tool indicators
//   - dependencies — known-vulnerable / abandoned / typosquatted
//     entries in requirements.txt, package.json, go.mod
func New() *Scanner {
	return NewWithChecks(
		NewPromptInjectionCheck(),
		NewSecretsCheck(),
		NewUnicodeCheck(),
		NewPermissionsCheck(),
		NewPatternsCheck(),
		NewMCPToolPoisoningCheck(),
		NewMCPLeastPrivilegeCheck(),
		NewSignatureCheck(),
		NewDependencyCheck(),
		NewCoverageCheck(),
	)
}

// NewWithChecks returns a scanner running exactly the supplied checks,
// in the order given. Useful for tests and for callers that want to
// disable a particular check without rebuilding the default set.
func NewWithChecks(checks ...Check) *Scanner {
	out := make([]Check, 0, len(checks))
	for _, c := range checks {
		if c == nil {
			continue
		}
		out = append(out, c)
	}
	return &Scanner{checks: out}
}

// AddLLM appends a semantic [LLMCheck] to the pipeline. The check
// runs only when the scanner has an LLMProvider configured (see
// [Scanner.WithLLMProvider]); otherwise it is a no-op.
func (s *Scanner) AddLLM(c LLMCheck) *Scanner {
	if c != nil {
		s.llmChecks = append(s.llmChecks, c)
	}
	return s
}

// WithLLMProvider attaches a provider for semantic checks. Pass nil
// to clear. Returns the scanner for chaining.
func (s *Scanner) WithLLMProvider(p LLMProvider) *Scanner {
	s.provider = p
	return s
}

// Checks returns the configured check names in pipeline order. The
// slice is a copy.
func (s *Scanner) Checks() []string {
	out := make([]string, 0, len(s.checks))
	for _, c := range s.checks {
		out = append(out, c.Name())
	}
	return out
}

// Summary counts findings per severity. Fields are JSON-tagged so the
// summary survives `qvr scan --output json` for downstream tooling.
type Summary struct {
	Critical int `json:"critical"`
	Error    int `json:"error"`
	Warning  int `json:"warning"`
	Info     int `json:"info"`
}

// Total is the sum across all severities.
func (s Summary) Total() int {
	return s.Critical + s.Error + s.Warning + s.Info
}

// MaxSeverity returns the highest severity present, or empty if no
// findings were recorded.
func (s Summary) MaxSeverity() Severity {
	switch {
	case s.Critical > 0:
		return SeverityCritical
	case s.Error > 0:
		return SeverityError
	case s.Warning > 0:
		return SeverityWarning
	case s.Info > 0:
		return SeverityInfo
	}
	return ""
}

// ScanResult is the top-level structured output of a scan run. The JSON
// shape is part of the public CLI contract — additive changes only.
//
// ScannedAt records when the scan was run, in RFC 3339 form with an
// explicit numeric offset (e.g. `2026-05-27T23:18:31.944085+00:00`)
// so timestamps remain unambiguous when reports cross machines and
// timezones.
//
// Components is the typed inventory of files inside the skill, taken
// before any checks ran. Consumers (skill cards, review packets,
// dashboards) use it to render the "what's in this skill?" view
// without re-walking the directory.
type ScanResult struct {
	Path       string      `json:"path"`
	Skill      string      `json:"skill"`
	ScannedAt  string      `json:"scanned_at,omitempty"`
	Checks     []string    `json:"checks"`
	Components []Component `json:"components,omitempty"`
	Findings   []Finding   `json:"findings"`
	Summary    Summary     `json:"summary"`
}

// scanTimestampLayout matches the wire shape requested in the public
// contract: ISO 8601 / RFC 3339 with microseconds and a numeric
// timezone offset (no `Z` shortcut). Equivalent to Python's
// `datetime.isoformat()`.
const scanTimestampLayout = "2006-01-02T15:04:05.999999-07:00"

// now is the clock seam used by [Scanner.Scan] to stamp ScannedAt.
// Tests override it to keep the JSON deterministic.
var now = time.Now

// Scan loads files from dir, runs every registered check against the
// skill, and returns a ScanResult with findings sorted by severity then
// file/line for deterministic output.
//
// dir is the on-disk skill directory; skill is the loaded model (see
// internal/skill.LoadFromPath). The two are passed separately so
// callers can scan a synthesised skill (for tests) without forcing a
// filesystem dance.
func (s *Scanner) Scan(ctx context.Context, skill *model.Skill, dir string) (*ScanResult, error) {
	scannedAt := now()

	files, err := WalkSkill(dir)
	if err != nil {
		return nil, fmt.Errorf("walk skill: %w", err)
	}

	var all []Finding
	for _, c := range s.checks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		findings := c.Run(ctx, skill, files)
		all = append(all, findings...)
	}
	if s.provider != nil {
		for _, c := range s.llmChecks {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			findings := c.Run(ctx, s.provider, skill, files)
			all = append(all, findings...)
		}
	}

	sortFindings(all)
	summary := summarise(all)

	skillName := ""
	if skill != nil {
		skillName = skill.Name
	}

	return &ScanResult{
		Path:       dir,
		Skill:      skillName,
		ScannedAt:  scannedAt.Format(scanTimestampLayout),
		Checks:     s.Checks(),
		Components: ComponentsFromFiles(files),
		Findings:   all,
		Summary:    summary,
	}, nil
}

// Filter returns a new slice keeping only findings at or above min.
// Used by the CLI to honour `--severity`.
func Filter(findings []Finding, min Severity) []Finding {
	cutoff := min.Rank()
	if cutoff <= 0 {
		// info or unknown — keep everything (info is the floor).
		return append([]Finding(nil), findings...)
	}
	out := make([]Finding, 0, len(findings))
	for _, f := range findings {
		if f.Severity.Rank() >= cutoff {
			out = append(out, f)
		}
	}
	return out
}

func summarise(findings []Finding) Summary {
	var s Summary
	for _, f := range findings {
		switch f.Severity {
		case SeverityCritical:
			s.Critical++
		case SeverityError:
			s.Error++
		case SeverityWarning:
			s.Warning++
		case SeverityInfo:
			s.Info++
		}
	}
	return s
}

func sortFindings(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		// Highest severity first.
		if a.Severity.Rank() != b.Severity.Rank() {
			return a.Severity.Rank() > b.Severity.Rank()
		}
		if a.Check != b.Check {
			return a.Check < b.Check
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
}
