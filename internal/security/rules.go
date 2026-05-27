package security

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// Category labels the SkillSpector / Quiver taxonomy bucket a [Rule]
// belongs to. Findings expose this in JSON so dashboards can group by
// category without re-parsing the rule ID.
type Category string

const (
	CategoryPromptInjection     Category = "prompt_injection"
	CategorySystemPromptLeakage Category = "system_prompt_leakage"
	CategoryDataExfiltration    Category = "data_exfiltration"
	CategoryPrivilegeEscalation Category = "privilege_escalation"
	CategorySupplyChain         Category = "supply_chain"
	CategoryExcessiveAgency     Category = "excessive_agency"
	CategoryOutputHandling      Category = "output_handling"
	CategoryMemoryPoisoning     Category = "memory_poisoning"
	CategoryToolMisuse          Category = "tool_misuse"
	CategoryRogueAgent          Category = "rogue_agent"
	CategoryHarmfulContent      Category = "harmful_content"
	CategoryTriggerAbuse        Category = "trigger_abuse"
	CategoryYARAMatch           Category = "yara_match"
	CategoryMCPLeastPrivilege   Category = "mcp_least_privilege"
	CategoryMCPToolPoisoning    Category = "mcp_tool_poisoning"
)

// Rule is one declarative pattern in the deterministic detection set.
//
// Globs is a list of doublestar-style globs (`**/*.py`, `*.md`) that
// scope the rule to certain files; nil/empty means every text file. The
// match is against the FileEntry.Path (forward-slash, skill-relative).
//
// Confidence is a 0.0-1.0 score that mirrors the SkillSpector taxonomy.
// It is exposed on findings so downstream tooling (UI, review packets)
// can sort or threshold without re-deriving from message text. It is not
// used by --severity / --fail-on, which act on Severity alone.
type Rule struct {
	ID          string
	Category    Category
	Severity    Severity
	Confidence  float64
	Globs       []string
	Pattern     string
	Hint        string
	Remediation string
	// SkipPattern, when non-empty, suppresses the rule on any line that
	// matches it. Used to keep doc-about-the-pattern lines quiet (e.g.
	// "this skill must NEVER ignore previous instructions" should not
	// fire P1). Like Pattern, source string compiles to a regex.
	SkipPattern string
	// FullFile, when true, runs Pattern against the whole file contents
	// (single match attempt) instead of line-by-line. Use for signatures
	// that span lines (PEM blocks, large base64 blobs).
	FullFile bool
}

// compiledRule is the runtime form of a [Rule] with compiled regexes
// and pre-parsed globs. Constructed by [RuleSet.Compile]; clients
// should not build it directly.
type compiledRule struct {
	rule Rule
	re   *regexp.Regexp
	skip *regexp.Regexp
}

// RuleSet is a slice of [Rule]s with helper methods. Order is preserved
// for deterministic finding output across runs.
type RuleSet []Rule

// FilterByCategory returns the rules whose Category matches any of the
// supplied categories. Useful for routing a subset of the registry to a
// dedicated check (e.g. prompt-injection findings emit under the legacy
// `prompt_injection` check name, while everything else flows through
// `patterns`).
func (rs RuleSet) FilterByCategory(cats ...Category) RuleSet {
	if len(cats) == 0 {
		return rs
	}
	wanted := make(map[Category]bool, len(cats))
	for _, c := range cats {
		wanted[c] = true
	}
	out := make(RuleSet, 0, len(rs))
	for _, r := range rs {
		if wanted[r.Category] {
			out = append(out, r)
		}
	}
	return out
}

// ExcludeCategory is the complement of FilterByCategory.
func (rs RuleSet) ExcludeCategory(cats ...Category) RuleSet {
	if len(cats) == 0 {
		return rs
	}
	skip := make(map[Category]bool, len(cats))
	for _, c := range cats {
		skip[c] = true
	}
	out := make(RuleSet, 0, len(rs))
	for _, r := range rs {
		if !skip[r.Category] {
			out = append(out, r)
		}
	}
	return out
}

// Compile returns a slice of compiledRule ready to run. Returns the
// first compile error with the offending rule ID for diagnostics.
func (rs RuleSet) Compile() ([]compiledRule, error) {
	out := make([]compiledRule, 0, len(rs))
	seen := make(map[string]bool, len(rs))
	for _, r := range rs {
		if seen[r.ID] {
			return nil, fmt.Errorf("duplicate rule id %q", r.ID)
		}
		seen[r.ID] = true

		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return nil, fmt.Errorf("rule %s: compile pattern: %w", r.ID, err)
		}
		c := compiledRule{rule: r, re: re}
		if r.SkipPattern != "" {
			skip, err := regexp.Compile(r.SkipPattern)
			if err != nil {
				return nil, fmt.Errorf("rule %s: compile skip: %w", r.ID, err)
			}
			c.skip = skip
		}
		out = append(out, c)
	}
	return out, nil
}

// MustCompile is the panic-on-error variant of [RuleSet.Compile],
// intended for package-init use against the built-in rule set.
func (rs RuleSet) MustCompile() []compiledRule {
	cr, err := rs.Compile()
	if err != nil {
		panic(err)
	}
	return cr
}

// AppliesTo reports whether the rule should run against the given
// skill-relative path. A rule with no globs applies to every file.
func (r Rule) AppliesTo(p string) bool {
	if len(r.Globs) == 0 {
		return true
	}
	for _, g := range r.Globs {
		if matchGlob(g, p) {
			return true
		}
	}
	return false
}

// matchGlob is a small doublestar matcher. It supports the forms we
// actually use in the rule set:
//
//   - bare basename glob: `*.py`, `requirements*.txt` — matches any
//     file whose basename matches
//   - anchored full-path glob: `tests/*.py`, `package.json`
//   - prefix-relative globs starting with `**/` or `**` — matches any
//     path whose suffix matches the rest
//
// We deliberately avoid a full doublestar implementation; the rule set
// can be expressed in these forms.
func matchGlob(pattern, name string) bool {
	if pattern == "" {
		return name == ""
	}
	// `**/X` — match any path ending in something that matches `X`.
	if strings.HasPrefix(pattern, "**/") {
		rest := pattern[3:]
		return suffixMatch(rest, name)
	}
	if pattern == "**" {
		return true
	}
	// Plain pattern: try as full-path glob first, then as basename
	// glob if the pattern has no separator.
	if ok, _ := path.Match(pattern, name); ok {
		return true
	}
	if !strings.Contains(pattern, "/") {
		if ok, _ := path.Match(pattern, path.Base(name)); ok {
			return true
		}
	}
	return false
}

// suffixMatch reports whether some suffix of name (broken on `/`) is
// matched by pat. Pat itself may contain `*`/`?` but no `**`.
func suffixMatch(pat, name string) bool {
	if ok, _ := path.Match(pat, name); ok {
		return true
	}
	for i := 0; i < len(name); i++ {
		if name[i] != '/' {
			continue
		}
		if ok, _ := path.Match(pat, name[i+1:]); ok {
			return true
		}
	}
	// Also try the basename for `**/*.py` style patterns.
	if !strings.Contains(pat, "/") {
		if ok, _ := path.Match(pat, path.Base(name)); ok {
			return true
		}
	}
	return false
}
