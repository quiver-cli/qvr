package privacy

import (
	"fmt"
	"regexp"
)

// RedactedMarker is the literal replacement string. Kept exported so
// tests can assert on the exact token and downstream code can detect
// already-redacted strings without re-scanning.
const RedactedMarker = "[REDACTED]"

// RegexChecker scans an event's string fields for secret-shaped
// substrings and produces redacted replacements. It never sets
// IsSensitive or StripContent — secret-in-source is orthogonal to
// sensitive-path. Callers compose this with PathChecker via Composite.
type RegexChecker struct {
	compiled []*regexp.Regexp
	labels   []string
}

// NewRegexChecker compiles the supplied patterns. Returns the first
// compile error encountered; the caller decides whether to surface or
// proceed with a partial set (production code surfaces).
func NewRegexChecker(patterns []NamedPattern) (*RegexChecker, error) {
	c := &RegexChecker{
		compiled: make([]*regexp.Regexp, 0, len(patterns)),
		labels:   make([]string, 0, len(patterns)),
	}
	for _, p := range patterns {
		r, err := regexp.Compile(p.Regex)
		if err != nil {
			return nil, fmt.Errorf("privacy: compile %q: %w", p.Label, err)
		}
		c.compiled = append(c.compiled, r)
		c.labels = append(c.labels, p.Label)
	}
	return c, nil
}

// Evaluate produces per-field redactions. A field appears in
// Decision.Redactions only if its value actually changed — unmodified
// fields are omitted to keep the mutation path cheap.
func (c *RegexChecker) Evaluate(e Event) Decision {
	if c == nil || len(c.compiled) == 0 {
		return Decision{}
	}
	fields := e.GetStringFields()
	if len(fields) == 0 {
		return Decision{}
	}

	redactions := map[string]string{}
	var matched []string

	for name, val := range fields {
		if val == "" {
			continue
		}
		red := val
		var localMatched []string
		for i, r := range c.compiled {
			if r.MatchString(red) {
				red = r.ReplaceAllString(red, RedactedMarker)
				localMatched = append(localMatched, c.labels[i])
			}
		}
		if red != val {
			redactions[name] = red
			matched = append(matched, localMatched...)
		}
	}

	if len(redactions) == 0 {
		return Decision{}
	}
	return Decision{
		Redactions:   redactions,
		MatchedRules: matched,
	}
}
