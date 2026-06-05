package security

import (
	"context"
	"fmt"
	"strings"

	"github.com/quiver-cli/qvr/internal/model"
)

// PatternsCheckName is the [Check.Name] of the unified pattern check.
// It emits findings tagged with a category so reports can group them by
// the detection taxonomy without re-parsing message text.
const PatternsCheckName = "patterns"

// patternCheck runs a pre-compiled [RuleSet] over every text file in a
// skill, emitting one finding per (rule, line) match. The check honours
// per-rule globs, per-line skip patterns, and full-file rules.
type patternCheck struct {
	name  string
	rules []compiledRule
}

// NewPatternsCheck returns the catch-all pattern check. It runs every
// built-in rule that isn't already covered by another dedicated check
// (prompt_injection, secrets, unicode, permissions).
func NewPatternsCheck() Check {
	rules := BuiltinRules().
		ExcludeCategory(CategoryPromptInjection, CategorySystemPromptLeakage).
		MustCompile()
	return &patternCheck{name: PatternsCheckName, rules: rules}
}

// NewRuleCheck returns a custom-named check running an arbitrary slice
// of rules. Used internally to retire the bespoke prompt-injection
// regex table while preserving its Check.Name() for callers that filter
// findings by check name.
func NewRuleCheck(name string, rules RuleSet) Check {
	return &patternCheck{name: name, rules: rules.MustCompile()}
}

func (p *patternCheck) Name() string { return p.name }

func (p *patternCheck) Run(_ context.Context, _ *model.Skill, files []FileEntry) []Finding {
	var findings []Finding
	for _, f := range files {
		if f.Content == "" {
			continue
		}
		applicable := make([]compiledRule, 0, len(p.rules))
		for _, r := range p.rules {
			if !r.rule.AppliesTo(f.Path) {
				continue
			}
			applicable = append(applicable, r)
		}
		if len(applicable) == 0 {
			continue
		}

		fullFile := make([]compiledRule, 0, len(applicable))
		lineRules := make([]compiledRule, 0, len(applicable))
		for _, r := range applicable {
			if r.rule.FullFile {
				fullFile = append(fullFile, r)
			} else {
				lineRules = append(lineRules, r)
			}
		}

		for _, r := range fullFile {
			loc := r.re.FindStringIndex(f.Content)
			if loc == nil {
				continue
			}
			if r.skip != nil && r.skip.MatchString(f.Content[loc[0]:loc[1]]) {
				continue
			}
			line := lineNumberFor(f.Content, loc[0])
			findings = append(findings, p.findingFor(r.rule, f.Path, line, lineTextFor(f.Content, loc[0])))
		}

		if len(lineRules) == 0 {
			continue
		}
		lines := strings.Split(f.Content, "\n")
		for lineIdx, line := range lines {
			for _, r := range lineRules {
				if !r.re.MatchString(line) {
					continue
				}
				if r.skip != nil && r.skip.MatchString(line) {
					continue
				}
				findings = append(findings, p.findingFor(r.rule, f.Path, lineIdx+1, line))
			}
		}
	}
	return findings
}

func (p *patternCheck) findingFor(r Rule, file string, line int, evidence string) Finding {
	msg := fmt.Sprintf("%s: %s", r.ID, r.Hint)
	return Finding{
		Check:       p.name,
		RuleID:      r.ID,
		Category:    r.Category,
		Severity:    r.Severity,
		Confidence:  r.Confidence,
		File:        file,
		Line:        line,
		Evidence:    evidenceSnippet(evidence),
		Message:     msg,
		Remediation: r.Remediation,
	}
}

// lineNumberFor returns the 1-indexed line of byte offset `off` in `s`.
// Used for full-file rules whose match span needs to be attributed to a
// specific line.
func lineNumberFor(s string, off int) int {
	if off <= 0 {
		return 1
	}
	if off > len(s) {
		off = len(s)
	}
	return 1 + strings.Count(s[:off], "\n")
}

// lineTextFor returns the full text of the line containing byte offset `off`
// in `s`, excluding the trailing newline. Used for full-file rules where the
// match is located by offset rather than by line iteration.
func lineTextFor(s string, off int) string {
	if off < 0 {
		off = 0
	}
	if off > len(s) {
		off = len(s)
	}
	start := strings.LastIndexByte(s[:off], '\n') + 1
	end := strings.IndexByte(s[off:], '\n')
	if end < 0 {
		return s[start:]
	}
	return s[start : off+end]
}

// maxEvidenceLen caps the offending-line snippet so a pathologically long
// minified line can't bloat a finding (and the JSON payload behind it).
const maxEvidenceLen = 200

// evidenceSnippet trims surrounding whitespace and rune-caps the matched line
// so it's safe to render inline next to a finding. Capping is rune-aware so a
// truncation can't split a multi-byte sequence (the unicode check cares).
func evidenceSnippet(line string) string {
	s := strings.TrimSpace(line)
	r := []rune(s)
	if len(r) > maxEvidenceLen {
		return string(r[:maxEvidenceLen]) + "…"
	}
	return s
}
