package privacy

// Composite OR-composes multiple checkers. Evaluate merges every
// sub-decision into one: IsSensitive and StripContent latch on if any
// sub-checker sets them; Redactions maps union (last-write-wins on
// duplicate keys, which is fine in practice because both checkers emit
// the same final redacted string for a given input); MatchedRules
// concatenates in iteration order.
//
// The merge is order-dependent on the observable level: MatchedRules
// preserves insertion order and Redactions is last-write-wins, so
// swapping checker order can yield a differently-ordered rules list
// (and, in the rare case of divergent redactions, a different value).
// Callers that need a canonical rendering should sort MatchedRules on
// the read side.
type Composite struct {
	checkers []Checker
}

// NewComposite wraps the given checkers, discarding any nil entries so
// Evaluate doesn't have to branch on nil during the hot loop. A
// zero-length Composite is valid and evaluates to a zero Decision.
func NewComposite(cs ...Checker) *Composite {
	filtered := make([]Checker, 0, len(cs))
	for _, c := range cs {
		if c != nil {
			filtered = append(filtered, c)
		}
	}
	return &Composite{checkers: filtered}
}

// Evaluate fans out to each sub-checker and merges the results.
func (c *Composite) Evaluate(e Event) Decision {
	if c == nil || len(c.checkers) == 0 {
		return Decision{}
	}
	merged := Decision{Redactions: map[string]string{}}
	for _, sub := range c.checkers {
		d := sub.Evaluate(e)
		if d.IsSensitive {
			merged.IsSensitive = true
		}
		if d.StripContent {
			merged.StripContent = true
		}
		for k, v := range d.Redactions {
			merged.Redactions[k] = v
		}
		if len(d.MatchedRules) > 0 {
			merged.MatchedRules = append(merged.MatchedRules, d.MatchedRules...)
		}
	}
	// Normalize: empty map → nil, so IsZero behaves intuitively.
	if len(merged.Redactions) == 0 {
		merged.Redactions = nil
	}
	return merged
}
