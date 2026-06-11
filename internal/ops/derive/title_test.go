package derive_test

import (
	"strings"
	"testing"

	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/google/uuid"
)

// metaTitle derives the session and returns its unified-model title.
func metaTitle(t *testing.T, rows []*ops.RawTrace) string {
	t.Helper()
	d, err := derive.DeriveSession(rows)
	if err != nil || d == nil {
		return ""
	}
	return d.Meta.Title
}

func TestSessionTitle(t *testing.T) {
	sid := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")

	t.Run("first user prompt becomes the title", func(t *testing.T) {
		rows := []*ops.RawTrace{
			row(sid, 0, `{"type":"user","timestamp":"2026-06-02T00:00:00.000Z","message":{"role":"user","content":"add a dark mode toggle"}}`),
			row(sid, 1, `{"type":"assistant","timestamp":"2026-06-02T00:00:01.000Z","message":{"role":"assistant","model":"claude-opus-4-8","usage":{"output_tokens":5},"content":[{"type":"text","text":"ok"}]}}`),
			row(sid, 2, `{"type":"user","timestamp":"2026-06-02T00:00:02.000Z","message":{"role":"user","content":"now add tests"}}`),
		}
		if got := metaTitle(t, rows); got != "add a dark mode toggle" {
			t.Errorf("title = %q, want %q", got, "add a dark mode toggle")
		}
	})

	t.Run("multiline prompt collapses to one line", func(t *testing.T) {
		rows := []*ops.RawTrace{
			row(sid, 0, `{"type":"user","timestamp":"2026-06-02T00:00:00.000Z","message":{"role":"user","content":"line one\n\n   line two"}}`),
		}
		if got := metaTitle(t, rows); got != "line one line two" {
			t.Errorf("title = %q, want collapsed single line", got)
		}
	})

	t.Run("long prompt is clipped with an ellipsis", func(t *testing.T) {
		long := strings.Repeat("a", 200)
		rows := []*ops.RawTrace{
			row(sid, 0, `{"type":"user","timestamp":"2026-06-02T00:00:00.000Z","message":{"role":"user","content":"`+long+`"}}`),
		}
		got := metaTitle(t, rows)
		if r := []rune(got); len(r) != 121 || r[120] != '…' { // default clip = 120 runes + ellipsis
			t.Errorf("title clip = %q (len %d), want 120 runes + ellipsis", got, len([]rune(got)))
		}
	})

	t.Run("no rows yields no derivation", func(t *testing.T) {
		d, err := derive.DeriveSession(nil)
		if err != nil || d != nil {
			t.Errorf("DeriveSession(nil) = (%v, %v), want (nil, nil)", d, err)
		}
	})

	t.Run("unknown agent yields an error", func(t *testing.T) {
		r := row(sid, 0, `{"type":"user","message":{"role":"user","content":"hi"}}`)
		r.AgentName = "totally-unknown-agent"
		if _, err := derive.DeriveSession([]*ops.RawTrace{r}); err == nil {
			t.Error("DeriveSession(unknown agent) should error")
		}
	})
}
