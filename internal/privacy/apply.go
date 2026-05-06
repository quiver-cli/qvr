package privacy

// Apply mutates the event according to the decision. It is the only
// place in the package that touches the event; checkers never mutate.
//
// Order: strip content first (cheaper, discards the fields we'd be
// redacting anyway), then apply redactions to whatever string fields
// survive. Double-application is a no-op: redaction replaces secrets
// with a literal [REDACTED] marker that no pattern matches, and
// StripContent on an already-stripped event is idempotent.
func Apply(e Event, d Decision) {
	if d.StripContent {
		e.StripContent()
	}
	if len(d.Redactions) > 0 {
		e.ApplyRedactions(d.Redactions)
	}
}
