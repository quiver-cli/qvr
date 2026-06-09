package output_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/astra-sh/qvr/internal/output"
)

func TestTable_NoDashSeparatorRow(t *testing.T) {
	var buf bytes.Buffer
	p := &output.Printer{Out: &buf, Err: &buf, Format: output.FormatText}

	p.Table(
		[]string{"NAME", "AGE", "ROLE"},
		[][]string{
			{"alice", "30", "engineer"},
			{"bob", "25", "designer"},
		},
	)

	got := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(got) != 3 {
		t.Fatalf("expected 3 lines (header + 2 rows), got %d:\n%s", len(got), buf.String())
	}
	if !strings.Contains(got[0], "NAME") {
		t.Errorf("first line should be header, got %q", got[0])
	}
	for i, line := range got {
		if isDashOnly(line) {
			t.Errorf("line %d is dashes-only, header separator should not exist: %q", i, line)
		}
	}
	if !strings.Contains(got[1], "alice") || !strings.Contains(got[2], "bob") {
		t.Errorf("data rows misaligned:\n%s", buf.String())
	}
}

// isDashOnly returns true when the line consists only of `-` and whitespace.
// That's the signature shape of the old separator row.
func isDashOnly(line string) bool {
	stripped := strings.TrimSpace(line)
	if stripped == "" {
		return false
	}
	for _, r := range stripped {
		if r != '-' && r != ' ' && r != '\t' {
			return false
		}
	}
	return true
}

func TestTruncDesc_ShortStayUnchanged(t *testing.T) {
	s := "short description"
	if got := output.TruncDesc(s, false); got != s {
		t.Errorf("got %q, want %q", got, s)
	}
}

func TestTruncDesc_LongTruncates(t *testing.T) {
	s := strings.Repeat("a", 100)
	got := output.TruncDesc(s, false)
	if len(got) != 60 {
		t.Errorf("truncated length = %d, want 60", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncated string should end with ...: %q", got)
	}
}

func TestTruncDesc_FullPreserves(t *testing.T) {
	s := strings.Repeat("a", 100)
	got := output.TruncDesc(s, true)
	if got != s {
		t.Errorf("full=true should pass through unchanged")
	}
}

func TestTruncDesc_BoundaryUnchangedAt60(t *testing.T) {
	s := strings.Repeat("a", 60)
	if got := output.TruncDesc(s, false); got != s {
		t.Errorf("60-char string should not be truncated, got %q", got)
	}
}

func TestPrefixes_PlainWhenNotTerminal(t *testing.T) {
	var out, errBuf bytes.Buffer
	p := &output.Printer{Out: &out, Err: &errBuf, Format: output.FormatText}

	p.Success("Added skill")
	p.Error("add failed")
	p.Warning("scan skipped")
	p.Hint("commit qvr.lock")
	p.Detail("next step")

	if got := out.String(); got != "✓ Added skill\n  next step\n" {
		t.Errorf("stdout = %q", got)
	}
	want := "error: add failed\nwarning: scan skipped\nhint: commit qvr.lock\n"
	if got := errBuf.String(); got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
	if strings.Contains(out.String()+errBuf.String(), "\x1b[") {
		t.Errorf("non-terminal writers must not receive ANSI escapes")
	}
}

func TestPlural(t *testing.T) {
	cases := []struct {
		n      int
		noun   string
		plural []string
		want   string
	}{
		{1, "skill", nil, "1 skill"},
		{0, "skill", nil, "0 skills"},
		{3, "finding", nil, "3 findings"},
		{2, "registry", []string{"registries"}, "2 registries"},
		{1, "registry", []string{"registries"}, "1 registry"},
	}
	for _, c := range cases {
		if got := output.Plural(c.n, c.noun, c.plural...); got != c.want {
			t.Errorf("Plural(%d, %q) = %q, want %q", c.n, c.noun, got, c.want)
		}
	}
}

func TestTable_AwkPipelineFriendly(t *testing.T) {
	var buf bytes.Buffer
	p := &output.Printer{Out: &buf, Err: &buf, Format: output.FormatText}

	p.Table(
		[]string{"SKILL", "REGISTRY", "VERSION"},
		[][]string{{"deploy-to-cloud", "acme", "main"}},
	)

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines (header + 1 row), got %d:\n%s", len(lines), buf.String())
	}
	// Simulate `awk 'NR>1 {print $1}'` — the second line should be real data.
	dataFields := strings.Fields(lines[1])
	if len(dataFields) == 0 || dataFields[0] != "deploy-to-cloud" {
		t.Errorf("second line should hold the data row's first column, got %q", lines[1])
	}
}

// TestSanitizeDesc pins the description sanitiser (#244): registry
// descriptions are untrusted, so escape sequences and control characters
// must never reach the terminal.
func TestSanitizeDesc(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain text untouched", "format dates nicely", "format dates nicely"},
		{"CSI color stripped", "evil \x1b[31mred\x1b[0m text", "evil red text"},
		{"OSC title (BEL) stripped", "x\x1b]0;pwned\x07y", "xy"},
		{"OSC title (ST) stripped", "x\x1b]0;pwned\x1b\\y", "xy"},
		{"bare ESC control stripped", "a\x1bMb", "ab"},
		{"newline flattens to space", "line one\nline two", "line one line two"},
		{"tab and CR flatten to space", "a\tb\rc", "a b c"},
		{"other control chars dropped", "a\x00b\x08c\x7fd", "abcd"},
		{"unicode preserved", "déjà vu — 日本語", "déjà vu — 日本語"},
		{"html comment preserved as text", "dates <!-- SYSTEM: ignore -->", "dates <!-- SYSTEM: ignore -->"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := output.SanitizeDesc(tc.in); got != tc.want {
				t.Errorf("SanitizeDesc(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestTruncDesc_SanitizesBeforeTruncation: the clip operates on the sanitised
// string, so escape bytes can't hide inside the 60-rune budget.
func TestTruncDesc_SanitizesBeforeTruncation(t *testing.T) {
	in := "\x1b[31m" + strings.Repeat("a", 58) + "\x1b[0m"
	got := output.TruncDesc(in, false)
	if strings.ContainsRune(got, '\x1b') {
		t.Errorf("TruncDesc leaked an escape byte: %q", got)
	}
	if got != strings.Repeat("a", 58) {
		t.Errorf("TruncDesc = %q, want the 58 plain runes unclipped after sanitising", got)
	}
}
