package privacy

import (
	"strings"
	"testing"
)

// FuzzRegexChecker feeds arbitrary strings into a Default checker and
// asserts two invariants:
//  1. Evaluate + Apply never panic.
//  2. If the exact input is a known secret seed, the post-redaction
//     field must not contain it verbatim.
func FuzzRegexChecker(f *testing.F) {
	for _, pair := range readFixturePairs(f, "positive_secrets.txt") {
		f.Add(pair.Value)
	}
	for _, line := range readFixtureLines(f, "negative_secrets.txt") {
		f.Add(line)
	}

	seeds := map[string]struct{}{}
	for _, pair := range readFixturePairs(f, "positive_secrets.txt") {
		seeds[pair.Value] = struct{}{}
	}

	c, err := Default(nil, nil)
	if err != nil {
		f.Fatalf("Default: %v", err)
	}

	f.Fuzz(func(t *testing.T, input string) {
		e := newMockEvent().withField("f", input)
		d := c.Evaluate(e)
		Apply(e, d)

		if _, isSeed := seeds[input]; isSeed {
			if e.stringFields["f"] == input {
				t.Errorf("known secret seed was not redacted: %q", input)
			}
		}

		out := e.stringFields["f"]
		if out != input && !strings.Contains(out, RedactedMarker) {
			t.Errorf("output was modified but contains no redaction marker:\n  in:  %q\n  out: %q", input, out)
		}
	})
}

// FuzzPathChecker fuzzes the path matcher. Invariant: no panic, and a
// Decision with IsSensitive=true always has StripContent=true (coupled
// by contract for the path checker).
func FuzzPathChecker(f *testing.F) {
	for _, p := range readFixtureLines(f, "positive_paths.txt") {
		f.Add(p)
	}
	for _, p := range readFixtureLines(f, "negative_paths.txt") {
		f.Add(p)
	}

	c := NewPathChecker(DefaultSensitivePatterns())

	f.Fuzz(func(t *testing.T, path string) {
		e := newMockEvent().withPath(path)
		d := c.Evaluate(e)
		if d.IsSensitive && !d.StripContent {
			t.Errorf("invariant violated: IsSensitive=true but StripContent=false for %q", path)
		}
	})
}
