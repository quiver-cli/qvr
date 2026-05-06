package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"unicode/utf8"
)

// Format represents the output format.
type Format string

const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

// Printer handles formatted output.
type Printer struct {
	Out    io.Writer
	Err    io.Writer
	Format Format
}

// New creates a new Printer with the given format.
func New(format Format) *Printer {
	return &Printer{
		Out:    os.Stdout,
		Err:    os.Stderr,
		Format: format,
	}
}

// Success prints a success message (text mode only).
func (p *Printer) Success(msg string) {
	if p.Format == FormatText {
		fmt.Fprintf(p.Out, "✓ %s\n", msg)
	}
}

// Error prints an error message (text mode only).
func (p *Printer) Error(msg string) {
	if p.Format == FormatText {
		fmt.Fprintf(p.Err, "✗ %s\n", msg)
	}
}

// Warning prints a warning message (text mode only).
func (p *Printer) Warning(msg string) {
	if p.Format == FormatText {
		fmt.Fprintf(p.Err, "! %s\n", msg)
	}
}

// Info prints an info message (text mode only).
func (p *Printer) Info(msg string) {
	if p.Format == FormatText {
		fmt.Fprintln(p.Out, msg)
	}
}

// JSON prints data as JSON. Works in both modes.
func (p *Printer) JSON(data any) error {
	enc := json.NewEncoder(p.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(data)
}

// TruncDesc renders a description for a text-mode table cell. In `full` mode
// it passes the string through unchanged; otherwise it clips to 60 chars with
// an ellipsis. Centralised here so search, list, and registry list all agree
// on the shape of truncation.
func TruncDesc(s string, full bool) string {
	if full || utf8.RuneCountInString(s) <= 60 {
		return s
	}
	runes := []rune(s)
	return string(runes[:57]) + "..."
}

// Table prints a table with headers and rows.
func (p *Printer) Table(headers []string, rows [][]string) {
	w := tabwriter.NewWriter(p.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, strings.Join(headers, "\t"))
	for _, row := range rows {
		fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	w.Flush()
}
