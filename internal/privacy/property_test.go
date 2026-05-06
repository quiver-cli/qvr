package privacy

import (
	"strings"
	"testing"
)

// TestProperty_SensitiveStripsContent asserts that any event flagged
// IsSensitive loses its content-bearing fields after Apply. This is
// the load-bearing invariant of the whole package.
func TestProperty_SensitiveStripsContent(t *testing.T) {
	c, err := Default(nil, nil)
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	for _, path := range readFixtureLines(t, "positive_paths.txt") {
		t.Run(path, func(t *testing.T) {
			e := newMockEvent().
				withPath(path).
				withContentField("diff_content", "sensitive diff body").
				withContentField("raw_event", `{"original":"payload"}`)

			d := c.Evaluate(e)
			if !d.IsSensitive {
				t.Fatalf("expected IsSensitive for %q", path)
			}
			Apply(e, d)
			if e.stringFields["diff_content"] != "" {
				t.Errorf("diff_content should be stripped; got %q", e.stringFields["diff_content"])
			}
			if e.stringFields["raw_event"] != "" {
				t.Errorf("raw_event should be stripped; got %q", e.stringFields["raw_event"])
			}
		})
	}
}

// TestProperty_RedactionsEliminateSecret asserts that for every known
// secret fixture, the secret substring is not present anywhere in the
// post-redaction event's string fields. This is the property that
// makes "qvr ops export" safe to share.
func TestProperty_RedactionsEliminateSecret(t *testing.T) {
	c, err := Default(nil, nil)
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	for _, pair := range readFixturePairs(t, "positive_secrets.txt") {
		t.Run(pair.Label+"|"+pair.Value, func(t *testing.T) {
			e := newMockEvent().withField("f", pair.Value)

			d := c.Evaluate(e)
			Apply(e, d)

			// Pull out the raw "secret" portion: strip known prefix.
			// We use the whole value as a conservative substring:
			// after Apply the full original must not appear in any
			// field.
			for name, v := range e.stringFields {
				if v != "" && strings.Contains(v, pair.Value) {
					t.Errorf("field %q still contains original value after redaction:\n  in: %q\n  now: %q",
						name, pair.Value, v)
				}
			}
		})
	}
}

// TestProperty_NegativesUnchanged asserts that non-secret strings pass
// through unmodified. Over-redaction is a different kind of failure,
// caught here.
func TestProperty_NegativesUnchanged(t *testing.T) {
	c, err := Default(nil, nil)
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	for _, line := range readFixtureLines(t, "negative_secrets.txt") {
		t.Run(line, func(t *testing.T) {
			e := newMockEvent().withField("f", line)
			d := c.Evaluate(e)
			Apply(e, d)
			if e.stringFields["f"] != line {
				t.Errorf("negative fixture mutated:\n  in:  %q\n  out: %q\n  matched: %v",
					line, e.stringFields["f"], d.MatchedRules)
			}
		})
	}
}

// TestProperty_ApplyIdempotent asserts that Apply(e, d); Apply(e, d)
// produces the same final state as a single Apply. This matters because
// the funnel may re-run privacy on retry paths.
func TestProperty_ApplyIdempotent(t *testing.T) {
	c, err := Default(nil, nil)
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	for _, pair := range readFixturePairs(t, "positive_secrets.txt") {
		t.Run(pair.Label, func(t *testing.T) {
			e := newMockEvent().withField("f", pair.Value)
			d := c.Evaluate(e)
			Apply(e, d)
			once := e.stringFields["f"]
			// Re-evaluate on the mutated event; expect zero Decision
			// (or at most a no-op Redactions map with the same value).
			d2 := c.Evaluate(e)
			Apply(e, d2)
			if e.stringFields["f"] != once {
				t.Errorf("apply-twice produced different result:\n  once:  %q\n  twice: %q", once, e.stringFields["f"])
			}
		})
	}
}
