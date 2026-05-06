package privacy

import (
	"strings"
	"testing"
)

func TestApply_StripContent(t *testing.T) {
	e := newMockEvent().
		withContentField("diff", "sensitive bytes here").
		withContentField("raw", `{"secret":"..."}`).
		withField("path", ".env") // path is metadata — must survive
	Apply(e, Decision{IsSensitive: true, StripContent: true})
	if e.stripCalled != 1 {
		t.Errorf("expected StripContent called once; got %d", e.stripCalled)
	}
	if e.stringFields["diff"] != "" {
		t.Errorf("expected diff zeroed; got %q", e.stringFields["diff"])
	}
	if e.stringFields["raw"] != "" {
		t.Errorf("expected raw zeroed; got %q", e.stringFields["raw"])
	}
	if e.stringFields["path"] != ".env" {
		t.Errorf("expected path preserved; got %q", e.stringFields["path"])
	}
}

func TestApply_Redactions(t *testing.T) {
	e := newMockEvent().
		withField("diff", "password=hunter2").
		withField("path", ".env")
	Apply(e, Decision{
		Redactions: map[string]string{"diff": "[REDACTED]"},
	})
	if e.redactCalled != 1 {
		t.Errorf("expected ApplyRedactions called once; got %d", e.redactCalled)
	}
	if e.stringFields["diff"] != "[REDACTED]" {
		t.Errorf("expected diff redacted; got %q", e.stringFields["diff"])
	}
	if e.stringFields["path"] != ".env" {
		t.Errorf("expected path untouched; got %q", e.stringFields["path"])
	}
}

func TestApply_Both(t *testing.T) {
	e := newMockEvent().
		withContentField("diff", "sensitive").
		withField("error", "bearer abc.def.ghi leaked")
	Apply(e, Decision{
		IsSensitive:  true,
		StripContent: true,
		Redactions:   map[string]string{"error": "bearer [REDACTED]"},
	})
	// StripContent runs before ApplyRedactions — so the content-field
	// "diff" is gone; but "error" (non-content) gets redacted.
	if e.stringFields["diff"] != "" {
		t.Errorf("expected diff stripped; got %q", e.stringFields["diff"])
	}
	if !strings.Contains(e.stringFields["error"], "[REDACTED]") {
		t.Errorf("expected error redacted; got %q", e.stringFields["error"])
	}
}

func TestApply_EmptyDecisionNoOp(t *testing.T) {
	e := newMockEvent().withField("f", "password=abc")
	Apply(e, Decision{})
	if e.stripCalled != 0 {
		t.Errorf("expected StripContent not called; got %d", e.stripCalled)
	}
	if e.redactCalled != 0 {
		t.Errorf("expected ApplyRedactions not called; got %d", e.redactCalled)
	}
	if e.stringFields["f"] != "password=abc" {
		t.Errorf("expected field unchanged; got %q", e.stringFields["f"])
	}
}

func TestApply_Idempotent(t *testing.T) {
	e := newMockEvent().withField("f", "password=hunter2")
	d := Decision{Redactions: map[string]string{"f": "[REDACTED]"}}
	Apply(e, d)
	first := e.stringFields["f"]
	Apply(e, d)
	if e.stringFields["f"] != first {
		t.Errorf("expected idempotent apply; got %q → %q", first, e.stringFields["f"])
	}
}
