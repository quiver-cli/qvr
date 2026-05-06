package ops

import "errors"

// Sentinel errors. Callers use errors.Is to discriminate.
var (
	// ErrNoPayload is returned by Event.DecodePayload when Payload is
	// nil or empty. Separate from a JSON decode error so callers can
	// treat absence as "no typed data available, fall back to raw".
	ErrNoPayload = errors.New("ops: event has no payload")

	// ErrUnknownAdapter is returned by the adapter registry when the
	// requested adapter name is not registered. The funnel logs this
	// via self_audits.
	ErrUnknownAdapter = errors.New("ops: unknown adapter")

	// ErrUnattributed is returned by the resolver when no skill can
	// be attributed to an event. The funnel drops the event and
	// records a self_audit.
	ErrUnattributed = errors.New("ops: event could not be attributed to a skill")
)
