// Package privacy implements the content-redaction and sensitive-path
// detection layer that runs before events are persisted.
//
// Privacy is intentionally decoupled from any specific event schema. The
// package defines a minimal Event interface (below); consumers (e.g.
// internal/ops) implement it on their own event type. This keeps privacy
// swappable and independently testable.
package privacy

// Event is the surface privacy needs from a pipeline event. Concrete
// event types implement this (e.g. *ops.Event). Keeping this interface
// tiny is the whole point of the package seam.
type Event interface {
	// GetPaths returns every filesystem path the event references. Used
	// by the path checker to decide sensitivity. May return nil.
	GetPaths() []string

	// GetStringFields returns the subset of the event's string-valued
	// fields that should be scanned for secrets. Key is a stable field
	// identifier ("payload.command", "diff_content", "error_message"),
	// value is the raw string. ApplyRedactions receives redacted values
	// keyed the same way.
	GetStringFields() map[string]string

	// StripContent zeroes the event's content-bearing fields while
	// preserving metadata (paths, timestamps, attribution). Called for
	// events flagged as sensitive by the path checker.
	StripContent()

	// ApplyRedactions replaces named string fields with redacted
	// equivalents (secret tokens → [REDACTED]). The map keys match
	// those returned by GetStringFields.
	ApplyRedactions(redactions map[string]string)
}
