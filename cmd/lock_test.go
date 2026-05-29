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

// The v2→v3 provenance backfill tests were removed when v5 hard-broke the
// migration path. v4-and-older locks now error out at ReadLockFile with
// "delete qvr.lock and run qvr sync" — see internal/model/lockfile_test.go's
// TestLockFile_RejectsUnsupportedVersion. `qvr lock upgrade` in v5 only
// backfills missing SubtreeHash on v5 entries (e.g. installs where the hash
// computation failed); that path is exercised in install integration tests.
