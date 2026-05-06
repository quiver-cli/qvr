package privacy

import "fmt"

// Checker decides what privacy action applies to an event. Implementations:
//   - PathChecker  — flags sensitive filesystem paths
//   - RegexChecker — redacts secret-shaped strings
//   - Composite    — OR-composes multiple checkers
//
// Callers typically construct a Checker via Default.
type Checker interface {
	Evaluate(e Event) Decision
}

// Default returns the production checker: sensitive-path detection
// composed with regex-based secret redaction. userSensitive and
// userRedact are merged on top of the built-in defaults; defaults
// cannot be subtracted. A nil slice is fine.
//
// Returns an error only when a user-supplied regex fails to compile;
// startup code should surface this loudly instead of silently losing
// a rule.
func Default(userSensitive []string, userRedact []string) (Checker, error) {
	paths := append([]string{}, DefaultSensitivePatterns()...)
	paths = append(paths, userSensitive...)

	regex := append([]NamedPattern{}, DefaultRedactPatterns()...)
	for i, u := range userRedact {
		regex = append(regex, NamedPattern{Label: fmt.Sprintf("user_%d", i), Regex: u})
	}

	rc, err := NewRegexChecker(regex)
	if err != nil {
		return nil, fmt.Errorf("privacy: build regex checker: %w", err)
	}
	return NewComposite(NewPathChecker(paths), rc), nil
}
