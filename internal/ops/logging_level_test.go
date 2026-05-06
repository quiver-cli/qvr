package ops

import (
	"encoding/json"
	"strings"
	"testing"
)

func buildCommandEvent(t *testing.T, stdout, stderr string) *Event {
	t.Helper()
	e := &Event{ActionType: ActionCommandExec, DiffContent: "some diff body"}
	if err := e.SetPayload(CommandExecPayload{
		Command: "ls",
		Stdout:  stdout,
		Stderr:  stderr,
	}); err != nil {
		t.Fatalf("SetPayload: %v", err)
	}
	return e
}

func payloadField(t *testing.T, e *Event, key string) string {
	t.Helper()
	if len(e.Payload) == 0 {
		return ""
	}
	var p map[string]any
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if v, ok := p[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func TestApplyLoggingLevel_FullPreservesEverything(t *testing.T) {
	longStdout := strings.Repeat("x", 10_000)
	e := buildCommandEvent(t, longStdout, "boom")
	ApplyLoggingLevel(e, LoggingLevelFull, LoggingCaps{StdoutMaxChars: 100})
	if got := payloadField(t, e, "stdout"); got != longStdout {
		t.Errorf("full level should not truncate; got len=%d want %d", len(got), len(longStdout))
	}
	if e.DiffContent != "some diff body" {
		t.Errorf("full level should not touch DiffContent")
	}
}

func TestApplyLoggingLevel_StandardTruncates(t *testing.T) {
	longStdout := strings.Repeat("x", 10_000)
	e := buildCommandEvent(t, longStdout, "y")
	ApplyLoggingLevel(e, LoggingLevelStandard, LoggingCaps{StdoutMaxChars: 50, StderrMaxChars: 50, ContextMaxChars: 200})
	got := payloadField(t, e, "stdout")
	if len(got) >= len(longStdout) {
		t.Errorf("expected truncation; got len=%d", len(got))
	}
	if !strings.Contains(got, "[truncated]") {
		t.Errorf("expected truncation marker; got %q", got[:min(80, len(got))])
	}
}

func TestApplyLoggingLevel_MinimalDropsContent(t *testing.T) {
	e := buildCommandEvent(t, "abc", "def")
	e.RawEvent = json.RawMessage(`{"raw":"bytes"}`)
	ApplyLoggingLevel(e, LoggingLevelMinimal, LoggingCaps{})
	if e.DiffContent != "" {
		t.Errorf("expected DiffContent cleared; got %q", e.DiffContent)
	}
	if e.RawEvent != nil {
		t.Errorf("expected RawEvent cleared; got %s", e.RawEvent)
	}
	if got := payloadField(t, e, "stdout"); got != "" {
		t.Errorf("expected stdout cleared; got %q", got)
	}
	if got := payloadField(t, e, "stderr"); got != "" {
		t.Errorf("expected stderr cleared; got %q", got)
	}
}

func TestApplyLoggingLevel_MinimalContentHash(t *testing.T) {
	e := buildCommandEvent(t, "abc", "")
	ApplyLoggingLevel(e, LoggingLevelMinimal, LoggingCaps{ContentHash: true})
	if !strings.HasPrefix(e.DiffContent, "sha256:") {
		t.Errorf("expected sha256 prefix on DiffContent; got %q", e.DiffContent)
	}
}

func TestApplyLoggingLevel_UnknownLevelFallsBackToStandard(t *testing.T) {
	long := strings.Repeat("x", 500)
	e := buildCommandEvent(t, long, "")
	ApplyLoggingLevel(e, "bogus-level", LoggingCaps{StdoutMaxChars: 10})
	if got := payloadField(t, e, "stdout"); len(got) >= len(long) {
		t.Errorf("expected fallback truncation; got len=%d", len(got))
	}
}

func TestTruncate_UnlimitedWhenMaxZero(t *testing.T) {
	s := strings.Repeat("x", 100)
	if got := truncate(s, 0); got != s {
		t.Errorf("max=0 should be unlimited; got len=%d", len(got))
	}
}

func TestTruncate_NoTruncationUnderLimit(t *testing.T) {
	s := "abc"
	if got := truncate(s, 10); got != s {
		t.Errorf("expected no-op; got %q", got)
	}
}

func TestTruncate_MarkerAppended(t *testing.T) {
	got := truncate("abcdefghij", 3)
	if !strings.HasSuffix(got, "[truncated]") {
		t.Errorf("expected suffix; got %q", got)
	}
	if !strings.HasPrefix(got, "abc") {
		t.Errorf("expected prefix abc; got %q", got)
	}
}

func TestTruncate_UTF8Safe(t *testing.T) {
	// 3 runes but 9 bytes. Truncating to 2 runes should give a valid
	// UTF-8 string, not a sliced-byte corrupted one.
	got := truncate("αβγ", 2)
	if !strings.HasPrefix(got, "αβ") {
		t.Errorf("expected αβ prefix; got %q", got)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
