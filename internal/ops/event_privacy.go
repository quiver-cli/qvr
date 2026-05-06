package ops

import (
	"encoding/json"
	"strings"
)

// This file makes *Event satisfy privacy.Event. The implementation is
// kept separate so the core Event struct in event.go stays legible.
//
// The four methods below are the entire contract between ops and
// internal/privacy — nothing else leaks across that seam.

// GetPaths extracts every filesystem path the event references, for
// the PathChecker to scan. The extraction is payload-type-aware: we
// peek at the ActionType and pull the path field of the matching
// typed payload shape (see payload.go).
//
// SkillPath is NOT included — it's a path inside the skill directory,
// which we already know is not sensitive by definition of how skills
// are attributed.
func (e *Event) GetPaths() []string {
	if e == nil || len(e.Payload) == 0 {
		return nil
	}
	// Pull every known path-bearing field at once. Cheaper than
	// switching on ActionType per event, and resilient to adapters
	// that mis-label action types.
	var peek struct {
		Path    string `json:"path,omitempty"`
		Cwd     string `json:"cwd,omitempty"`
		Pattern string `json:"pattern,omitempty"`
		// ToolUse.Input may contain a "path" or "file_path" key — we
		// handle that via a second pass below so we don't pull the
		// whole map.
	}
	_ = json.Unmarshal(e.Payload, &peek) // best-effort

	out := make([]string, 0, 3)
	if peek.Path != "" {
		out = append(out, peek.Path)
	}
	if peek.Cwd != "" {
		out = append(out, peek.Cwd)
	}
	if peek.Pattern != "" {
		out = append(out, peek.Pattern)
	}

	// ToolUse input commonly carries a path under one of several names.
	if e.ActionType == ActionToolUse {
		var tu ToolUsePayload
		if err := json.Unmarshal(e.Payload, &tu); err == nil {
			for _, key := range []string{"path", "file_path", "filename", "target"} {
				if v, ok := tu.Input[key]; ok {
					if s, ok := v.(string); ok && s != "" {
						out = append(out, s)
					}
				}
			}
		}
	}

	return out
}

// GetStringFields returns the subset of the event's string-valued
// fields that should be scanned for secrets. Keys are stable
// identifiers so ApplyRedactions knows which fields to rewrite.
//
// The map includes payload-level strings (Path, Cwd, Stdout, Stderr,
// NewString, OldString, Body, Output, Message, Command) in addition to
// the top-level DiffContent and ErrorMessage.
func (e *Event) GetStringFields() map[string]string {
	out := map[string]string{}
	if e == nil {
		return out
	}
	if e.DiffContent != "" {
		out["diff_content"] = e.DiffContent
	}
	if e.ErrorMessage != "" {
		out["error_message"] = e.ErrorMessage
	}
	if len(e.RawEvent) > 0 {
		out["raw_event"] = string(e.RawEvent)
	}
	if len(e.Payload) > 0 {
		// Payload fields worth scanning. We extract by key to keep the
		// map keys stable across payload types.
		var peek map[string]any
		if err := json.Unmarshal(e.Payload, &peek); err == nil {
			for _, key := range []string{
				"command", "cwd", "path",
				"stdout", "stderr",
				"new_string", "old_string", "content_preview",
				"body", "output", "message", "prompt",
			} {
				if v, ok := peek[key]; ok {
					if s, ok := v.(string); ok && s != "" {
						out["payload."+key] = s
					}
				}
			}
		}
	}
	return out
}

// StripContent zeroes the event's content-bearing fields while leaving
// metadata (paths, timestamps, attribution) intact. Called when a
// sensitive path is detected; the event's metadata still flows through
// so operators can see "skill X touched .env at time T" without the
// diff bytes.
//
// Idempotent: calling twice is a no-op.
func (e *Event) StripContent() {
	if e == nil {
		return
	}
	e.DiffContent = ""
	e.RawEvent = nil
	if len(e.Payload) == 0 {
		return
	}
	// Decode → zero content-bearing keys → re-encode. We don't know
	// the exact payload type, so we operate on the generic map.
	// Fail-safe on decode error: drop the payload rather than leave
	// potentially-sensitive bytes intact. StripContent is the privacy
	// escape hatch — it must not be able to leak.
	var p map[string]any
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		e.Payload = nil
		return
	}
	for _, key := range []string{
		"stdout", "stderr",
		"new_string", "old_string", "content_preview",
		"body", "output", "message", "prompt",
	} {
		if _, ok := p[key]; ok {
			p[key] = ""
		}
	}
	buf, err := json.Marshal(p)
	if err != nil {
		return
	}
	e.Payload = buf
}

// ApplyRedactions rewrites the named string fields with redacted
// values. Keys match those returned by GetStringFields.
//
// Fields that are already empty at call time are NOT re-populated.
// This preserves the StripContent invariant: once content is cleared
// (for a sensitive-path event), a subsequent redaction pass can't
// resurrect it with a [REDACTED] marker.
func (e *Event) ApplyRedactions(redactions map[string]string) {
	if e == nil || len(redactions) == 0 {
		return
	}

	if v, ok := redactions["diff_content"]; ok && e.DiffContent != "" {
		e.DiffContent = v
	}
	if v, ok := redactions["error_message"]; ok && e.ErrorMessage != "" {
		e.ErrorMessage = v
	}
	if v, ok := redactions["raw_event"]; ok && len(e.RawEvent) > 0 {
		// The redacted string is no longer guaranteed to be valid
		// JSON — regex replacement may break quoting/structure. Wrap
		// it as a JSON string literal so downstream marshalers keep
		// working. If the redacted value happens to still be valid
		// JSON we preserve it as-is.
		if json.Valid([]byte(v)) {
			e.RawEvent = json.RawMessage(v)
		} else if b, err := json.Marshal(v); err == nil {
			e.RawEvent = json.RawMessage(b)
		} else {
			e.RawEvent = nil
		}
	}

	// Payload rewrites: decode, swap in redacted strings, re-encode.
	// We only touch keys with a "payload." prefix so other redactions
	// don't leak into unrelated fields.
	if len(e.Payload) == 0 {
		return
	}
	var hasPayloadRedactions bool
	for k := range redactions {
		if strings.HasPrefix(k, "payload.") {
			hasPayloadRedactions = true
			break
		}
	}
	if !hasPayloadRedactions {
		return
	}

	var p map[string]any
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return
	}
	for k, v := range redactions {
		if !strings.HasPrefix(k, "payload.") {
			continue
		}
		field := strings.TrimPrefix(k, "payload.")
		cur, ok := p[field]
		if !ok {
			continue
		}
		// Mirror the top-level-field rule: don't resurrect a field
		// that StripContent already zeroed.
		if s, ok := cur.(string); ok && s == "" {
			continue
		}
		p[field] = v
	}
	buf, err := json.Marshal(p)
	if err != nil {
		return
	}
	e.Payload = buf
}
