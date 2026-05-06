package privacy

// Decision is the output of Checker.Evaluate. It is a plain value: the
// caller applies it via Apply. Separating evaluation from mutation keeps
// the checkers pure and the pipeline stage testable.
type Decision struct {
	// IsSensitive marks the event as touching sensitive content (e.g.
	// a secret file). Downstream code stores this bit alongside the
	// event so it can be filtered and surfaced.
	IsSensitive bool

	// StripContent instructs Apply to zero content-bearing fields on
	// the event. Always set together with IsSensitive when the path
	// checker fires; kept as a separate field so future checkers can
	// flag an event as sensitive without stripping (e.g. notification
	// events that have no content to strip).
	StripContent bool

	// Redactions maps field identifiers (as returned by
	// Event.GetStringFields) to their redacted form. Empty when no
	// regex matched.
	Redactions map[string]string

	// MatchedRules is an observability aid — the labels or patterns
	// that fired for this event. Not persisted; useful in tests and
	// log lines.
	MatchedRules []string
}

// IsZero reports whether the decision is the zero value (nothing fired).
func (d Decision) IsZero() bool {
	return !d.IsSensitive && !d.StripContent && len(d.Redactions) == 0 && len(d.MatchedRules) == 0
}
