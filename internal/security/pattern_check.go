package security

import (
	"context"
	"fmt"
	"strings"

	"github.com/raks097/quiver/internal/model"
)

// PatternsCheckName is the [Check.Name] of the unified pattern check.
// It emits findings tagged with a category so reports can group them by
// the SkillSpector taxonomy without re-parsing message text.
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
			findings = append(findings, p.findingFor(r.rule, f.Path, line))
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
				findings = append(findings, p.findingFor(r.rule, f.Path, lineIdx+1))
			}
		}
	}
	return findings
}

func (p *patternCheck) findingFor(r Rule, file string, line int) Finding {
	msg := fmt.Sprintf("%s: %s", r.ID, r.Hint)
	return Finding{
		Check:       p.name,
		RuleID:      r.ID,
		Category:    r.Category,
		Severity:    r.Severity,
		Confidence:  r.Confidence,
		File:        file,
		Line:        line,
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
