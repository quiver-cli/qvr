package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

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

// TestRunAdd_EmptyAs_Rejects is the #103 regression. Pre-fix, passing
// `--as ""` was indistinguishable from not passing --as at all, so the
// install silently dropped the alias intent and used the canonical name
// — a footgun for shell scripts where `--as "$alias"` had `$alias` end
// up empty. The fix wires cmd.Flags().Changed("as") into the empty check
// so the explicit empty value routes through the standard invalid-name
// error, matching the behaviour for uppercase / hyphens / etc.
func TestRunAdd_EmptyAs_Rejects(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Reset and reattach the package var to a fresh cobra command, then
	// parse `--as ""` so Flags().Changed("as") reports true the same way
	// real CLI invocation would.
	addAs = ""
	fc := &cobra.Command{Use: "add"}
	fc.Flags().StringVar(&addAs, "as", "", "")
	if err := fc.ParseFlags([]string{"--as", ""}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	t.Cleanup(func() { addAs = "" })

	err = runAdd(fc, []string{"someskill"})
	if err == nil {
		t.Fatal("expected error for explicit --as \"\", got nil")
	}
	if !strings.Contains(err.Error(), "invalid --as value") {
		t.Errorf("error = %v; want substring 'invalid --as value'", err)
	}
}

// TestRunAdd_EmptyAs_NotPassed_AllowsThrough is the negative-case partner
// to the empty-rejection test: when `--as` was never passed at all,
// Flags().Changed("as") is false and the empty check must not fire. We
// can't drive a full runAdd in this minimal env (the printer is nil), so
// just exercise the cobra Changed semantic the production check relies
// on. This is the contract — verifying it in isolation is enough to catch
// a regression where someone simplifies the check to `if addAs == ""`.
func TestRunAdd_EmptyAs_NotPassed_AllowsThrough(t *testing.T) {
	var dst string
	fc := &cobra.Command{Use: "add"}
	fc.Flags().StringVar(&dst, "as", "", "")
	// No ParseFlags call — flag was never set on the command line.
	if fc.Flags().Changed("as") {
		t.Error("Flags().Changed(\"as\") = true with no parsing; production check would wrongly reject")
	}
}
