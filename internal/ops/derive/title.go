package derive

import (
	"encoding/json"
	"strings"
)

// titleDefaultMaxLen is the clip length finalizeMeta uses for session titles.
const titleDefaultMaxLen = 120

// firstPromptFromSpans returns a human-friendly session title: the first
// prompt the user typed into the session, collapsed to a single trimmed line
// and clipped. It reads the spans the deriver produced, so it reuses every
// agent's transcript-parsing logic for free — the title is the user content of
// the session's first LLM turn.
//
// Returns "" when the session has no LLM turn or an empty first prompt;
// callers fall back to a placeholder ("untitled session") rather than showing
// nothing.
func firstPromptFromSpans(spans []Span, maxLen int) string {
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
			if isScaffoldPrompt(m.Content) {
				continue // harness preamble, not what the user typed
			}
			if t := cleanTitle(m.Content); t != "" {
				return clipTitle(t, maxLen)
			}
		}
	}
	return ""
}

// isScaffoldPrompt reports whether a "user" message is harness scaffolding
// rather than typed input: local-command caveat blocks and slash-command
// envelopes. Skipping them lets the first REAL prompt title the session.
func isScaffoldPrompt(s string) bool {
	t := strings.TrimSpace(s)
	for _, prefix := range []string{
		"<local-command-caveat>",
		"Caveat: the messages below",
		"<command-name>",
		"<local-command-stdout>",
	} {
		if len(t) >= len(prefix) && strings.EqualFold(t[:len(prefix)], prefix) {
			return true
		}
	}
	return false
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
