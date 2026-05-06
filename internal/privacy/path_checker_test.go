package privacy

import (
	"testing"
)

func TestPathChecker_Defaults_Positive(t *testing.T) {
	c := NewPathChecker(DefaultSensitivePatterns())
	for _, path := range readFixtureLines(t, "positive_paths.txt") {
		t.Run(path, func(t *testing.T) {
			e := newMockEvent().withPath(path)
			d := c.Evaluate(e)
			if !d.IsSensitive {
				t.Errorf("expected IsSensitive=true for %q; got false", path)
			}
			if !d.StripContent {
				t.Errorf("expected StripContent=true for %q; got false", path)
			}
			if len(d.MatchedRules) == 0 {
				t.Errorf("expected MatchedRules non-empty for %q", path)
			}
		})
	}
}

func TestPathChecker_Defaults_Negative(t *testing.T) {
	c := NewPathChecker(DefaultSensitivePatterns())
	for _, path := range readFixtureLines(t, "negative_paths.txt") {
		t.Run(path, func(t *testing.T) {
			e := newMockEvent().withPath(path)
			d := c.Evaluate(e)
			if d.IsSensitive {
				t.Errorf("expected IsSensitive=false for %q; got true (matched: %v)", path, d.MatchedRules)
			}
			if d.StripContent {
				t.Errorf("expected StripContent=false for %q; got true", path)
			}
		})
	}
}

func TestPathChecker_EmptyPathsNoMatch(t *testing.T) {
	c := NewPathChecker(DefaultSensitivePatterns())
	d := c.Evaluate(newMockEvent())
	if !d.IsZero() {
		t.Errorf("expected zero Decision for event with no paths; got %+v", d)
	}
}

func TestPathChecker_EmptyPatternsNoMatch(t *testing.T) {
	c := NewPathChecker(nil)
	d := c.Evaluate(newMockEvent().withPath(".env"))
	if !d.IsZero() {
		t.Errorf("expected zero Decision with nil patterns; got %+v", d)
	}
}

func TestPathChecker_StopsOnFirstMatch(t *testing.T) {
	c := NewPathChecker([]string{"**/.env", "**/*.pem"})
	e := newMockEvent().withPath(".env").withPath("private.pem")
	d := c.Evaluate(e)
	if !d.IsSensitive {
		t.Fatalf("expected sensitive")
	}
	if len(d.MatchedRules) != 1 {
		t.Errorf("expected one rule fired (stops on first match); got %v", d.MatchedRules)
	}
}

func TestPathChecker_MalformedPatternSkipped(t *testing.T) {
	// doublestar.PathMatch returns an error for patterns with unclosed
	// brackets. The checker should skip and continue.
	c := NewPathChecker([]string{"[unclosed", "**/.env"})
	e := newMockEvent().withPath("/project/.env")
	d := c.Evaluate(e)
	if !d.IsSensitive {
		t.Errorf("expected sensitive via second pattern; got %+v", d)
	}
}

func TestPathChecker_SkipsEmptyPath(t *testing.T) {
	c := NewPathChecker(DefaultSensitivePatterns())
	e := newMockEvent().withPath("").withPath(".env")
	d := c.Evaluate(e)
	if !d.IsSensitive {
		t.Errorf("expected sensitive (second path matches); got %+v", d)
	}
}

func TestNormalizePath(t *testing.T) {
	cases := []struct{ in, want string }{
		{".env", ".env"},
		{".ENV", ".env"},
		{"  .env  ", ".env"},
		{"Secrets/db.yaml", "secrets/db.yaml"},
		{"secrets/", "secrets"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := normalizePath(tc.in); got != tc.want {
				t.Errorf("normalizePath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
