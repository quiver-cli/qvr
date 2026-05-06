package ops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// ApplyLoggingLevel truncates or strips event fields based on the
// configured logging level. It runs AFTER privacy — a sensitive event
// has already had content zeroed, so truncation on an empty string is
// a no-op.
//
// Levels:
//   - minimal:  drop DiffContent, RawEvent, and payload stdout/stderr
//     entirely. Replace with content hash if cfg.ContentHash is true.
//   - standard: truncate large content fields to the configured caps.
//   - full:     preserve everything (bounded only by the adapter itself).
//
// Unknown levels fall back to standard. Passing a zero config (all
// fields unset) behaves as standard with built-in caps — ApplyDefaults
// fills those in.
func ApplyLoggingLevel(e *Event, level string, caps LoggingCaps) {
	if e == nil {
		return
	}
	switch level {
	case LoggingLevelFull:
		return
	case LoggingLevelMinimal:
		applyMinimal(e, caps.ContentHash)
	default:
		// standard (or unknown)
		applyStandard(e, caps)
	}
}

// LoggingCaps is the subset of OpsLoggingConfig this package consumes.
// Passing a narrow struct (instead of the full config) keeps the
// level-truncation path unit-testable without a config.Config.
type LoggingCaps struct {
	StdoutMaxChars  int
	StderrMaxChars  int
	ContextMaxChars int
	ContentHash     bool
}

func applyMinimal(e *Event, hash bool) {
	if hash {
		if e.DiffContent != "" {
			e.DiffContent = "sha256:" + hashString(e.DiffContent)
		}
		if len(e.RawEvent) > 0 {
			e.RawEvent = json.RawMessage([]byte(`"sha256:` + hashString(string(e.RawEvent)) + `"`))
		}
	} else {
		e.DiffContent = ""
		e.RawEvent = nil
	}
	// Zero stdout/stderr/output in payload.
	if len(e.Payload) == 0 {
		return
	}
	var p map[string]any
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return
	}
	for _, key := range []string{"stdout", "stderr", "output", "body"} {
		if _, ok := p[key]; ok {
			p[key] = ""
		}
	}
	if buf, err := json.Marshal(p); err == nil {
		e.Payload = buf
	}
}

func applyStandard(e *Event, caps LoggingCaps) {
	if caps.ContextMaxChars > 0 {
		e.DiffContent = truncate(e.DiffContent, caps.ContextMaxChars)
	}
	if len(e.Payload) == 0 {
		return
	}
	var p map[string]any
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return
	}
	truncateField := func(key string, limit int) {
		if limit <= 0 {
			return
		}
		if v, ok := p[key]; ok {
			if s, ok := v.(string); ok {
				p[key] = truncate(s, limit)
			}
		}
	}
	truncateField("stdout", caps.StdoutMaxChars)
	truncateField("stderr", caps.StderrMaxChars)
	truncateField("output", caps.ContextMaxChars)
	truncateField("body", caps.ContextMaxChars)
	truncateField("content_preview", caps.ContextMaxChars)
	if buf, err := json.Marshal(p); err == nil {
		e.Payload = buf
	}
}

// truncate returns s cut to at most max runes, with a trailing marker
// when truncation occurred. Zero max means unlimited.
func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	// Work in runes to avoid slicing a UTF-8 multibyte sequence.
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max]) + "…[truncated]"
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
