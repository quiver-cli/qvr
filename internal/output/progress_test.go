package output

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

// A spinner bound to a non-terminal writer (the common case in tests, pipes,
// and CI) must never write anything — stdout/stderr stay clean and JSON
// streams uncorrupted.
func TestSpinner_DisabledIsNoOp(t *testing.T) {
	var buf bytes.Buffer
	p := &Printer{Out: &buf, Err: &buf, Format: FormatText}

	sp := p.Spinner()
	sp.Start("cloning")
	sp.Update("scanning")
	sp.Interrupt(func() { /* would print under a real TTY */ })
	sp.Stop()

	if buf.Len() != 0 {
		t.Fatalf("disabled spinner wrote %q, want nothing", buf.String())
	}
}

// JSON mode disables the spinner even on a terminal so the data stream stays
// pure.
func TestPrinterSpinner_JSONModeDisabled(t *testing.T) {
	p := &Printer{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, Format: FormatJSON}
	if p.Spinner().enabled {
		t.Fatal("spinner should be disabled in JSON mode")
	}
}

// Interrupt on a disabled spinner must still run the supplied function so
// callers can route warnings through it unconditionally.
func TestSpinner_InterruptRunsFnWhenDisabled(t *testing.T) {
	sp := newSpinner(&bytes.Buffer{}, false)
	ran := false
	sp.Interrupt(func() { ran = true })
	if !ran {
		t.Fatal("Interrupt must invoke fn even when the spinner is disabled")
	}
}

// When enabled, the spinner renders to its writer, Update changes the label,
// Interrupt clears the line before printing, and Stop wipes the trailing line.
func TestSpinner_EnabledRendersAndClears(t *testing.T) {
	buf := &lockedBuffer{}
	sp := newSpinner(buf, true)

	sp.Start("cloning acme/skills")
	sp.Update("scanning [1/2] foo")
	sp.Interrupt(func() { _, _ = buf.WriteString("! warning\n") })
	sp.Stop()

	out := buf.String()
	if !strings.Contains(out, "cloning acme/skills") && !strings.Contains(out, "scanning") {
		t.Fatalf("expected a rendered label in output, got %q", out)
	}
	if !strings.Contains(out, "! warning") {
		t.Fatalf("Interrupt should have emitted the warning, got %q", out)
	}
	// In-place rendering means the output is driven by carriage returns rather
	// than newlines per frame.
	if !strings.Contains(out, "\r") {
		t.Fatalf("expected carriage-return-driven in-place rendering, got %q", out)
	}
}

// lockedBuffer is a concurrency-safe bytes.Buffer — the spinner's render
// goroutine and the test goroutine both write to it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) WriteString(s string) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.WriteString(s)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
