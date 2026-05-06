package privacy

import (
	"strings"
	"testing"
)

func TestDefault_MergesUserPatterns(t *testing.T) {
	c, err := Default(
		[]string{"**/*.custom-secret"},
		[]string{`custom_token\s*=\s*\S+`},
	)
	if err != nil {
		t.Fatalf("Default: %v", err)
	}

	// User path pattern fires.
	d := c.Evaluate(newMockEvent().withPath("app.custom-secret"))
	if !d.IsSensitive {
		t.Errorf("expected custom path pattern to fire; got %+v", d)
	}

	// Default path pattern still fires.
	d = c.Evaluate(newMockEvent().withPath(".env"))
	if !d.IsSensitive {
		t.Errorf("expected default .env to still fire; got %+v", d)
	}

	// User regex pattern fires.
	d = c.Evaluate(newMockEvent().withField("f", "custom_token = abc123"))
	if len(d.Redactions) == 0 {
		t.Errorf("expected custom regex pattern to fire; got %+v", d)
	}
	if !contains(d.MatchedRules, "user_0") {
		t.Errorf("expected user_0 label in %v", d.MatchedRules)
	}

	// Default regex pattern still fires.
	d = c.Evaluate(newMockEvent().withField("f", "password=hunter2"))
	if len(d.Redactions) == 0 {
		t.Errorf("expected default password pattern to still fire; got %+v", d)
	}
}

func TestDefault_BadUserRegexSurfaces(t *testing.T) {
	_, err := Default(nil, []string{"[unclosed"})
	if err == nil {
		t.Errorf("expected compile error to surface from Default")
	}
	if err != nil && !strings.Contains(err.Error(), "privacy:") {
		t.Errorf("expected error prefix 'privacy:'; got %v", err)
	}
}

func TestDefault_NilInputsIsValid(t *testing.T) {
	c, err := Default(nil, nil)
	if err != nil {
		t.Fatalf("Default(nil,nil): %v", err)
	}
	// Built-in defaults still apply.
	d := c.Evaluate(newMockEvent().withPath(".env"))
	if !d.IsSensitive {
		t.Errorf("expected default patterns active; got %+v", d)
	}
}

func TestDefault_DefaultsAreTheFloor(t *testing.T) {
	// Even with user patterns supplied, the default sensitive set
	// cannot be shrunk. Verify by evaluating each default-positive
	// fixture against a Default built with user overrides.
	c, err := Default([]string{"**/*.extra"}, []string{})
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	for _, p := range readFixtureLines(t, "positive_paths.txt") {
		d := c.Evaluate(newMockEvent().withPath(p))
		if !d.IsSensitive {
			t.Errorf("default-positive path %q lost sensitivity under user config", p)
		}
	}
}

func TestDecision_IsZero(t *testing.T) {
	if !(Decision{}).IsZero() {
		t.Errorf("empty Decision should be zero")
	}
	if (Decision{IsSensitive: true}).IsZero() {
		t.Errorf("IsSensitive=true should not be zero")
	}
	if (Decision{Redactions: map[string]string{"a": "b"}}).IsZero() {
		t.Errorf("non-empty Redactions should not be zero")
	}
}
