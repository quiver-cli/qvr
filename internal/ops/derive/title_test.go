package derive_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/quiver-cli/qvr/internal/ops"
	"github.com/quiver-cli/qvr/internal/ops/derive"
)

func TestFirstPrompt(t *testing.T) {
	sid := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")

	t.Run("first user prompt becomes the title", func(t *testing.T) {
		rows := []*ops.RawTrace{
			row(sid, 0, `{"type":"user","timestamp":"2026-06-02T00:00:00.000Z","message":{"role":"user","content":"add a dark mode toggle"}}`),
			row(sid, 1, `{"type":"assistant","timestamp":"2026-06-02T00:00:01.000Z","message":{"role":"assistant","model":"claude-opus-4-8","usage":{"output_tokens":5},"content":[{"type":"text","text":"ok"}]}}`),
			row(sid, 2, `{"type":"user","timestamp":"2026-06-02T00:00:02.000Z","message":{"role":"user","content":"now add tests"}}`),
		}
		if got := derive.FirstPrompt(rows, 100); got != "add a dark mode toggle" {
			t.Errorf("FirstPrompt = %q, want %q", got, "add a dark mode toggle")
		}
	})

	t.Run("multiline prompt collapses to one line", func(t *testing.T) {
		rows := []*ops.RawTrace{
			row(sid, 0, `{"type":"user","timestamp":"2026-06-02T00:00:00.000Z","message":{"role":"user","content":"line one\n\n   line two"}}`),
		}
		if got := derive.FirstPrompt(rows, 100); got != "line one line two" {
			t.Errorf("FirstPrompt = %q, want collapsed single line", got)
		}
	})

	t.Run("long prompt is clipped with an ellipsis", func(t *testing.T) {
		long := strings.Repeat("a", 200)
		rows := []*ops.RawTrace{
			row(sid, 0, `{"type":"user","timestamp":"2026-06-02T00:00:00.000Z","message":{"role":"user","content":"`+long+`"}}`),
		}
		got := derive.FirstPrompt(rows, 20)
		if r := []rune(got); len(r) != 21 || r[20] != '…' { // 20 chars + ellipsis
			t.Errorf("FirstPrompt clip = %q (len %d), want 20 runes + ellipsis", got, len([]rune(got)))
		}
	})

	t.Run("no rows yields empty title", func(t *testing.T) {
		if got := derive.FirstPrompt(nil, 100); got != "" {
			t.Errorf("FirstPrompt(nil) = %q, want empty", got)
		}
	})

	t.Run("unknown agent yields empty title", func(t *testing.T) {
		r := row(sid, 0, `{"type":"user","message":{"role":"user","content":"hi"}}`)
		r.AgentName = "totally-unknown-agent"
		if got := derive.FirstPrompt([]*ops.RawTrace{r}, 100); got != "" {
			t.Errorf("FirstPrompt(unknown agent) = %q, want empty", got)
		}
	})
}
