package output

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

// spinnerFrames is the Braille-dot animation used for in-place progress.
// Same set uv/cargo use — renders cleanly in a single terminal cell.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinnerInterval is how often the glyph advances. Fast enough to read as
// motion (so the terminal never looks frozen), slow enough to avoid churn.
const spinnerInterval = 90 * time.Millisecond

// Spinner renders an animated single-line progress indicator on a writer.
//
// It is a no-op when the target writer is not an interactive terminal (piped
// output, CI, tests) or when the owning Printer is in JSON mode. Callers can
// therefore drive Start/Update/Stop unconditionally and rely on the final
// Success/Info line for non-interactive output — nothing here ever touches
// stdout or corrupts a JSON stream.
type Spinner struct {
	w       io.Writer
	enabled bool
	style   Styler

	mu    sync.Mutex
	label string
	last  int // rune width of the last rendered line, for clean clearing
	stop  chan struct{}
	done  chan struct{}
}

func newSpinner(w io.Writer, enabled bool) *Spinner {
	return &Spinner{w: w, enabled: enabled, style: NewStyler(w)}
}

// Start begins the animation with an initial label. Calling Start on an
// already-running spinner just updates the label.
func (s *Spinner) Start(label string) {
	if s == nil || !s.enabled {
		return
	}
	s.mu.Lock()
	if s.stop != nil {
		s.label = label
		s.mu.Unlock()
		return
	}
	s.label = label
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	stop, done := s.stop, s.done
	s.mu.Unlock()

	go s.run(stop, done)
}

func (s *Spinner) run(stop, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(spinnerInterval)
	defer ticker.Stop()

	frame := 0
	s.render(spinnerFrames[frame])
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			frame = (frame + 1) % len(spinnerFrames)
			s.render(spinnerFrames[frame])
		}
	}
}

func (s *Spinner) render(glyph string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Width math uses the unstyled line — ANSI escapes occupy no cells, so
	// styling must not leak into the clear/pad accounting.
	line := glyph + " " + s.label
	width := len([]rune(line))
	pad := ""
	if n := s.last - width; n > 0 {
		pad = strings.Repeat(" ", n)
	}
	fmt.Fprintf(s.w, "\r%s %s%s", s.style.Cyan(glyph), s.label, pad)
	s.last = width
}

// Update changes the label shown by a running spinner. No-op if not running.
func (s *Spinner) Update(label string) {
	if s == nil || !s.enabled {
		return
	}
	s.mu.Lock()
	s.label = label
	s.mu.Unlock()
}

// Interrupt clears the current spinner line, runs fn (which may print its own
// line to the same writer), then lets the animation resume on the next tick.
// Use this to emit a warning mid-spin without the \r-driven line clobbering
// the message. Safe to call while the spinner is animating; fn runs under the
// same lock as the renderer so the two never interleave on the writer.
func (s *Spinner) Interrupt(fn func()) {
	if s == nil || !s.enabled {
		fn()
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last > 0 {
		fmt.Fprintf(s.w, "\r%s\r", strings.Repeat(" ", s.last))
	}
	s.last = 0
	fn()
}

// Stop halts the animation and clears the line. Idempotent.
func (s *Spinner) Stop() {
	if s == nil || !s.enabled {
		return
	}
	s.mu.Lock()
	stop, done := s.stop, s.done
	s.stop, s.done = nil, nil
	s.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	<-done

	s.mu.Lock()
	if s.last > 0 {
		fmt.Fprintf(s.w, "\r%s\r", strings.Repeat(" ", s.last))
	}
	s.last = 0
	s.mu.Unlock()
}

// Spinner returns a Spinner bound to the Printer's Err writer (so it never
// pollutes stdout). It animates only in text mode on an interactive terminal;
// in every other mode the returned spinner is a no-op.
func (p *Printer) Spinner() *Spinner {
	enabled := p.Format == FormatText && writerIsTerminal(p.Err)
	return newSpinner(p.Err, enabled)
}

func writerIsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}
