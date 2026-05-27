package cmd

import (
	"testing"
)

// Regression for #20 + #21: the message names actual failing categories
// rather than the legacy hardcoded "drift detected".
func TestFailureCategories_onlyNonZeroListed(t *testing.T) {
	cases := []struct {
		name string
		in   VerifySummary
		want string
	}{
		{"missing only", VerifySummary{Missing: 1}, "missing=1"},
		{"drift + missing", VerifySummary{Drift: 2, Missing: 1}, "drift=2, missing=1"},
		{"failed only", VerifySummary{Failed: 3}, "failed=3"},
		{"drift + failed", VerifySummary{Drift: 1, Failed: 2}, "drift=1, failed=2"},
		{"empty", VerifySummary{}, "no failing entries"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := failureCategories(c.in)
			if got != c.want {
				t.Errorf("failureCategories(%+v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
