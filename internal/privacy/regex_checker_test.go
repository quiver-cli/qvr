package privacy

import (
	"strings"
	"testing"
)

// buildDefaultRegexChecker compiles the production default pattern set.
// Tests that need different rule lists construct their own.
func buildDefaultRegexChecker(t *testing.T) *RegexChecker {
	t.Helper()
	c, err := NewRegexChecker(DefaultRedactPatterns())
	if err != nil {
		t.Fatalf("compile defaults: %v", err)
	}
	return c
}

func TestRegexChecker_Defaults_Positive(t *testing.T) {
	c := buildDefaultRegexChecker(t)
	for _, pair := range readFixturePairs(t, "positive_secrets.txt") {
		t.Run(pair.Label+"|"+pair.Value, func(t *testing.T) {
			e := newMockEvent().withField("f", pair.Value)
			d := c.Evaluate(e)

			if len(d.Redactions) == 0 {
				t.Fatalf("expected redactions for %q; got none", pair.Value)
			}
			if !contains(d.MatchedRules, pair.Label) {
				t.Errorf("expected MatchedRules to contain %q; got %v", pair.Label, d.MatchedRules)
			}
			red := d.Redactions["f"]
			if !strings.Contains(red, RedactedMarker) {
				t.Errorf("expected %s marker in redacted output %q", RedactedMarker, red)
			}
			// Secret substring must not appear verbatim in output. We
			// use the portion of the value that is the actual secret
			// (after the label=/ prefix). For simplicity assert the
			// full original value is absent — any surviving substring
			// of size ≥8 that came from the input is a bug.
			if pair.Value != red && strings.Contains(red, pair.Value) {
				t.Errorf("redacted output still contains original secret:\n  in:  %q\n  out: %q", pair.Value, red)
			}
		})
	}
}

func TestRegexChecker_Defaults_Negative(t *testing.T) {
	c := buildDefaultRegexChecker(t)
	for _, line := range readFixtureLines(t, "negative_secrets.txt") {
		t.Run(line, func(t *testing.T) {
			e := newMockEvent().withField("f", line)
			d := c.Evaluate(e)
			if len(d.Redactions) != 0 {
				t.Errorf("expected no redactions for %q; got %v (matched: %v)", line, d.Redactions, d.MatchedRules)
			}
		})
	}
}

func TestRegexChecker_BadRegexReturnsError(t *testing.T) {
	_, err := NewRegexChecker([]NamedPattern{{Label: "bad", Regex: "[unclosed"}})
	if err == nil {
		t.Errorf("expected compile error for malformed regex")
	}
}

func TestRegexChecker_EmptyPatternsNoMatch(t *testing.T) {
	c, err := NewRegexChecker(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	d := c.Evaluate(newMockEvent().withField("f", "password=hunter2"))
	if !d.IsZero() {
		t.Errorf("expected zero Decision with nil patterns; got %+v", d)
	}
}

func TestRegexChecker_EmptyFieldsNoMatch(t *testing.T) {
	c := buildDefaultRegexChecker(t)
	d := c.Evaluate(newMockEvent())
	if !d.IsZero() {
		t.Errorf("expected zero Decision with no fields; got %+v", d)
	}
}

func TestRegexChecker_IgnoresEmptyField(t *testing.T) {
	c := buildDefaultRegexChecker(t)
	e := newMockEvent().withField("f", "").withField("g", "password=hunter2")
	d := c.Evaluate(e)
	if _, ok := d.Redactions["f"]; ok {
		t.Errorf("expected no redaction for empty field")
	}
	if _, ok := d.Redactions["g"]; !ok {
		t.Errorf("expected redaction for non-empty field")
	}
}

func TestRegexChecker_OnlyChangedFieldsRedacted(t *testing.T) {
	c := buildDefaultRegexChecker(t)
	e := newMockEvent().
		withField("clean", "just some prose").
		withField("dirty", "password=abc123").
		withField("alsoclean", "nothing to see")
	d := c.Evaluate(e)
	if _, ok := d.Redactions["clean"]; ok {
		t.Errorf("expected clean field to be absent from redactions")
	}
	if _, ok := d.Redactions["alsoclean"]; ok {
		t.Errorf("expected alsoclean field to be absent from redactions")
	}
	if _, ok := d.Redactions["dirty"]; !ok {
		t.Errorf("expected dirty field to be redacted")
	}
}

func TestRegexChecker_MultiplePatternsOneField(t *testing.T) {
	// A field containing BOTH a password assignment and a GitHub PAT.
	// Both patterns should fire; the final redacted string should
	// contain neither secret.
	c := buildDefaultRegexChecker(t)
	val := "password=hunter2 and also ghp_1234567890abcdefghijklmnopqrstuvwxyz"
	d := c.Evaluate(newMockEvent().withField("f", val))
	red := d.Redactions["f"]
	if strings.Contains(red, "hunter2") {
		t.Errorf("password still present: %q", red)
	}
	if strings.Contains(red, "ghp_1234") {
		t.Errorf("github pat still present: %q", red)
	}
	if !contains(d.MatchedRules, "password") || !contains(d.MatchedRules, "github_pat") {
		t.Errorf("expected both labels in MatchedRules; got %v", d.MatchedRules)
	}
}

func TestRegexChecker_RedactionIdempotent(t *testing.T) {
	// Applying redaction twice should be a no-op: the [REDACTED]
	// marker doesn't match any production pattern.
	c := buildDefaultRegexChecker(t)
	e := newMockEvent().withField("f", "password=secret")
	d1 := c.Evaluate(e)
	// Simulate mutation then re-run.
	for k, v := range d1.Redactions {
		e.stringFields[k] = v
	}
	d2 := c.Evaluate(e)
	if !d2.IsZero() {
		t.Errorf("expected zero Decision on re-evaluation of redacted event; got %+v", d2)
	}
}

func TestRegexChecker_KnownLimitation_PasswordFnCall(t *testing.T) {
	// Documented false-positive: "password = fn()" fires the generic
	// password regex because it has the shape password[:=]<non-space>.
	// This test pins the behaviour so any future fix makes a deliberate
	// change (and a matching doc update).
	c := buildDefaultRegexChecker(t)
	e := newMockEvent().withField("f", "password = loadPassword()")
	d := c.Evaluate(e)
	if len(d.Redactions) == 0 {
		t.Fatalf("expected known-limitation fire; got nothing — update DefaultRedactPatterns and this test together if behaviour changed")
	}
}
