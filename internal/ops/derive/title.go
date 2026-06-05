package derive

import (
	"encoding/json"
	"strings"

	"github.com/quiver-cli/qvr/internal/ops"
)

// FirstPrompt returns a human-friendly session title: the first prompt the user
// typed into the session, collapsed to a single trimmed line and clipped. It is
// derived from the same spans the deriver produces, so it reuses every agent's
// transcript-parsing logic for free — the title is the user content of the
// session's first LLM turn.
//
// Returns "" when the session has no registered deriver (e.g. an agent we don't
// yet derive), no LLM turn, or an empty first prompt. Callers fall back to a
// placeholder ("untitled session") in that case rather than showing nothing.
func FirstPrompt(rows []*ops.RawTrace, maxLen int) string {
	spans, err := DeriveSession(rows)
	if err != nil || len(spans) == 0 {
		return ""
	}
	for _, sp := range spans {
		if sp.Kind != KindLLM {
			continue
		}
		raw, ok := sp.Attributes["gen_ai.input.messages"].(string)
		if !ok || raw == "" {
			continue
		}
		var msgs []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(raw), &msgs); err != nil {
			continue
		}
		for _, m := range msgs {
			// Only the user's own text titles a session — never injected
			// system/developer context. Today every deriver emits a single
			// user message here, but guarding on role keeps the title correct
			// if one ever prepends other roles.
			if m.Role != "user" {
				continue
			}
			if t := cleanTitle(m.Content); t != "" {
				return clipTitle(t, maxLen)
			}
		}
	}
	return ""
}

// cleanTitle collapses whitespace/newlines into single spaces and trims, so a
// multi-line prompt renders as one tidy line in a table cell.
func cleanTitle(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// clipTitle truncates to maxLen runes, appending an ellipsis when it cuts.
func clipTitle(s string, maxLen int) string {
	if maxLen <= 0 {
		maxLen = 120
	}
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return strings.TrimRight(string(r[:maxLen]), " ") + "…"
}
