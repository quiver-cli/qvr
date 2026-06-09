package output

import (
	"fmt"
	"io"
	"os"
)

// Styler renders ANSI-styled fragments for a single writer. The zero value is
// a disabled styler that passes text through unchanged, so Printer literals in
// tests (and any piped/redirected stream) stay plain.
//
// Styling is resolved per writer, not per process: stdout and stderr are
// detected independently, matching what uv/cargo do when one stream is piped
// and the other is a terminal.
type Styler struct {
	on bool
}

// NewStyler returns a Styler for w, enabled only when w is an interactive
// terminal, NO_COLOR is unset, and TERM is not "dumb".
func NewStyler(w io.Writer) Styler {
	return Styler{on: colorEnabled(w)}
}

func colorEnabled(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	return writerIsTerminal(w)
}

// wrap surrounds text with an SGR sequence and a reset. Empty text stays
// empty so callers can style optional fragments unconditionally.
func (s Styler) wrap(code, text string) string {
	if !s.on || text == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

// Bold renders text bold.
func (s Styler) Bold(text string) string { return s.wrap("1", text) }

// Dim renders text de-emphasised (faint).
func (s Styler) Dim(text string) string { return s.wrap("2", text) }

// Red renders text red.
func (s Styler) Red(text string) string { return s.wrap("31", text) }

// Green renders text green.
func (s Styler) Green(text string) string { return s.wrap("32", text) }

// Yellow renders text yellow.
func (s Styler) Yellow(text string) string { return s.wrap("33", text) }

// Cyan renders text cyan.
func (s Styler) Cyan(text string) string { return s.wrap("36", text) }

// BoldRed renders text bold red — the `error:` prefix style.
func (s Styler) BoldRed(text string) string { return s.wrap("1;31", text) }

// BoldGreen renders text bold green.
func (s Styler) BoldGreen(text string) string { return s.wrap("1;32", text) }

// BoldYellow renders text bold yellow — the `warning:` prefix style.
func (s Styler) BoldYellow(text string) string { return s.wrap("1;33", text) }

// BoldCyan renders text bold cyan — the `hint:` prefix style.
func (s Styler) BoldCyan(text string) string { return s.wrap("1;36", text) }

// Plural formats a count with its noun: Plural(1, "skill") → "1 skill",
// Plural(3, "skill") → "3 skills". Pass an explicit plural form for
// irregulars: Plural(2, "registry", "registries"). Centralised so command
// output never prints the "%d thing(s)" shape.
func Plural(n int, noun string, plural ...string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	p := noun + "s"
	if len(plural) > 0 {
		p = plural[0]
	}
	return fmt.Sprintf("%d %s", n, p)
}
