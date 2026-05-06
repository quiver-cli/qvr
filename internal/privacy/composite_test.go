package privacy

import (
	"testing"
)

// stubChecker returns a fixed Decision for every Evaluate call.
type stubChecker struct{ d Decision }

func (s stubChecker) Evaluate(Event) Decision { return s.d }

func TestComposite_Empty(t *testing.T) {
	c := NewComposite()
	d := c.Evaluate(newMockEvent())
	if !d.IsZero() {
		t.Errorf("expected zero Decision; got %+v", d)
	}
}

func TestComposite_NilReceiver(t *testing.T) {
	// Defensive: a nil *Composite should not panic.
	var c *Composite
	d := c.Evaluate(newMockEvent())
	if !d.IsZero() {
		t.Errorf("expected zero Decision from nil composite; got %+v", d)
	}
}

func TestComposite_OnlyPathFires(t *testing.T) {
	path := stubChecker{d: Decision{IsSensitive: true, StripContent: true, MatchedRules: []string{"**/.env"}}}
	regex := stubChecker{d: Decision{}}
	c := NewComposite(path, regex)
	d := c.Evaluate(newMockEvent())
	if !d.IsSensitive || !d.StripContent {
		t.Errorf("expected sensitive+strip; got %+v", d)
	}
	if len(d.Redactions) != 0 {
		t.Errorf("expected no redactions; got %v", d.Redactions)
	}
}

func TestComposite_OnlyRegexFires(t *testing.T) {
	path := stubChecker{d: Decision{}}
	regex := stubChecker{d: Decision{
		Redactions:   map[string]string{"f": "[REDACTED]"},
		MatchedRules: []string{"password"},
	}}
	c := NewComposite(path, regex)
	d := c.Evaluate(newMockEvent())
	if d.IsSensitive || d.StripContent {
		t.Errorf("expected non-sensitive; got %+v", d)
	}
	if len(d.Redactions) != 1 {
		t.Errorf("expected one redaction; got %v", d.Redactions)
	}
}

func TestComposite_BothFire(t *testing.T) {
	path := stubChecker{d: Decision{IsSensitive: true, StripContent: true, MatchedRules: []string{"**/.env"}}}
	regex := stubChecker{d: Decision{
		Redactions:   map[string]string{"f": "[REDACTED]"},
		MatchedRules: []string{"password"},
	}}
	c := NewComposite(path, regex)
	d := c.Evaluate(newMockEvent())
	if !d.IsSensitive || !d.StripContent {
		t.Errorf("expected sensitive+strip; got %+v", d)
	}
	if len(d.Redactions) != 1 {
		t.Errorf("expected redaction merged in; got %v", d.Redactions)
	}
	if !contains(d.MatchedRules, "**/.env") || !contains(d.MatchedRules, "password") {
		t.Errorf("expected both rule labels; got %v", d.MatchedRules)
	}
}

func TestComposite_OrderIndependent(t *testing.T) {
	path := stubChecker{d: Decision{IsSensitive: true, StripContent: true, MatchedRules: []string{"p"}}}
	regex := stubChecker{d: Decision{
		Redactions:   map[string]string{"f": "[REDACTED]"},
		MatchedRules: []string{"r"},
	}}
	a := NewComposite(path, regex).Evaluate(newMockEvent())
	b := NewComposite(regex, path).Evaluate(newMockEvent())
	if a.IsSensitive != b.IsSensitive || a.StripContent != b.StripContent {
		t.Errorf("sensitive/strip flags should be order-independent; got %+v vs %+v", a, b)
	}
	if len(a.Redactions) != len(b.Redactions) {
		t.Errorf("redactions should be equal; got %v vs %v", a.Redactions, b.Redactions)
	}
	// MatchedRules can differ in order but must contain the same labels.
	if !contains(a.MatchedRules, "p") || !contains(a.MatchedRules, "r") ||
		!contains(b.MatchedRules, "p") || !contains(b.MatchedRules, "r") {
		t.Errorf("expected both labels in both orders; got %v vs %v", a.MatchedRules, b.MatchedRules)
	}
}

func TestComposite_DuplicateRedactionsMerged(t *testing.T) {
	a := stubChecker{d: Decision{Redactions: map[string]string{"f": "A"}, MatchedRules: []string{"a"}}}
	b := stubChecker{d: Decision{Redactions: map[string]string{"f": "B"}, MatchedRules: []string{"b"}}}
	d := NewComposite(a, b).Evaluate(newMockEvent())
	if v, ok := d.Redactions["f"]; !ok {
		t.Errorf("expected f in redactions")
	} else if v != "A" && v != "B" {
		t.Errorf("expected last-write-wins; got %q", v)
	}
}
