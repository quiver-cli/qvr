package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/raks097/quiver/internal/skill"
)

// Regression for #66: errTextHandled must be a distinct sentinel value
// that wraps cleanly through errors.Is. Execute() relies on this to
// suppress the duplicate `Error: ...` line that Cobra otherwise prints
// after the per-skill `✗ add <name>: <reason>` line already surfaced
// the failure. If a future refactor accidentally reuses errJSONHandled
// or a plain string error, the suppression goes away and CI logs of
// partial-failure batches look like total failures.
func TestErrTextHandled_SentinelDistinctAndWrappable(t *testing.T) {
	if errors.Is(errTextHandled, errJSONHandled) {
		t.Error("errTextHandled and errJSONHandled must be distinct")
	}
	wrapped := fmt.Errorf("add demo: %w", errTextHandled)
	if !errors.Is(wrapped, errTextHandled) {
		t.Error("errTextHandled must survive errors.Is unwrap")
	}
	// A plain unrelated error must NOT match the sentinel — otherwise
	// Execute() would silently swallow real errors from other commands.
	if errors.Is(errors.New("nope"), errTextHandled) {
		t.Error("errTextHandled must not match unrelated errors")
	}
}

// TestBuildAddJSONEnvelope locks the JSON shape `qvr add --output json` emits
// for each of the three outcomes the bug #54 fix has to cover:
//
//   - all-success: {"installed": [...]}, no `error` key
//   - all-fail:    {"installed": [], "error": "..."}
//   - partial:     {"installed": [...], "error": "..."}
//
// The key shape promise is that `installed` is always an array (never null) so
// `jq '.installed[]'` works uniformly. Before #54 the all-fail case emitted
// the bare literal `null`.
func TestBuildAddJSONEnvelope(t *testing.T) {
	one := []*skill.InstallResult{{Name: "tdd", Version: "main"}}

	cases := []struct {
		name    string
		results []*skill.InstallResult
		err     error
		want    string
	}{
		{
			name:    "all-success",
			results: one,
			err:     nil,
			want:    `{"installed":[{"name":"tdd","registry":"","version":"main","worktree":"","targets":null,"commit":""}]}`,
		},
		{
			name:    "all-fail",
			results: nil,
			err:     errors.New("nope"),
			want:    `{"installed":[],"error":"nope"}`,
		},
		{
			name:    "partial",
			results: one,
			err:     errors.New("one-failed"),
			want:    `{"installed":[{"name":"tdd","registry":"","version":"main","worktree":"","targets":null,"commit":""}],"error":"one-failed"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := buildAddJSONEnvelope(tc.results, tc.err)
			b, err := json.Marshal(env)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(b) != tc.want {
				t.Fatalf("envelope mismatch\nwant: %s\ngot:  %s", tc.want, b)
			}
		})
	}
}
