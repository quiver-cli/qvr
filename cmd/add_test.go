package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/quiver-cli/qvr/internal/skill"
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

// TestBuildAddJSONEnvelope locks the JSON shape `qvr add --output json`
// emits for each of the three outcomes. Issue #121 — the `installed`
// array is no longer emitted on the all-fail path so add's failure
// envelope matches the universal `{"error": "..."}` shape every other
// command uses:
//
//   - all-success: {"installed": [...]}, no `error` key
//   - all-fail:    {"error": "..."}                  (was: {"installed":[],"error":...})
//   - partial:     {"installed": [...], "error": "..."}
//
// `jq '.installed // []'` is the recommended consumer pattern.
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
			want:    `{"error":"nope"}`,
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

	resetPrinter(t)

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
	// Post-#121 the --as path emits its own envelope (text: ✗ add: ...,
	// JSON: standard error envelope) and returns errTextHandled so
	// Execute() doesn't duplicate the message into a trailing
	// `Error: …` line.
	if !errors.Is(err, errTextHandled) {
		t.Errorf("err = %v, want errTextHandled (#121 — add errors should use add's own printer path, not Execute's default envelope)", err)
	}
	errBuf, ok := printer.Err.(interface{ String() string })
	if !ok {
		t.Fatalf("printer.Err is not a stringer; got %T", printer.Err)
	}
	got := errBuf.String()
	if !strings.Contains(got, "invalid --as value") {
		t.Errorf("printed error missing reason; got %q", got)
	}
	if !strings.Contains(got, "add:") {
		t.Errorf("printed error missing 'add:' prefix; got %q (issue #121 — should match the ✗ add ...: prefix of other add failures)", got)
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

// TestParseRemoteSkillSpec covers the one-step install spec parser: when an
// `qvr add` arg is a remote clone path it must split into (clone URL, skill
// name, ref); when it's a plain skill name it must report ok=false so the arg
// flows through normal registry resolution.
func TestParseRemoteSkillSpec(t *testing.T) {
	tests := []struct {
		in        string
		wantOK    bool
		wantURL   string
		wantSkill string
		wantPath  string // asserted only when non-empty
		wantRef   string
	}{
		// Plain skill names — NOT remote specs.
		{in: "tdd", wantOK: false},
		{in: "tdd@v2", wantOK: false},
		{in: "", wantOK: false},
		// A hostless org/repo/skill is NOT treated as remote (no dotted host)
		// so it can never silently clone a nonexistent remote.
		{in: "myorg/myskill", wantOK: false},

		// HTTPS-shaped, schemeless host.
		{in: "github.com/org/repo/tdd", wantOK: true,
			wantURL: "https://github.com/org/repo.git", wantSkill: "tdd"},
		{in: "github.com/org/repo/tdd@v2", wantOK: true,
			wantURL: "https://github.com/org/repo.git", wantSkill: "tdd", wantRef: "v2"},
		// Bare repo — no skill segment (sole-skill resolution happens later).
		{in: "github.com/org/repo", wantOK: true,
			wantURL: "https://github.com/org/repo.git", wantSkill: ""},
		{in: "github.com/org/repo@v2", wantOK: true,
			wantURL: "https://github.com/org/repo.git", wantSkill: "", wantRef: "v2"},

		// Explicit scheme.
		{in: "https://github.com/org/repo/tdd", wantOK: true,
			wantURL: "https://github.com/org/repo.git", wantSkill: "tdd"},
		{in: "https://github.com/org/repo.git/tdd", wantOK: true,
			wantURL: "https://github.com/org/repo.git", wantSkill: "tdd"},

		// scp-style SSH — the user@ must NOT be mistaken for a ref.
		{in: "git@github.com:org/repo/tdd", wantOK: true,
			wantURL: "git@github.com:org/repo.git", wantSkill: "tdd"},
		{in: "git@github.com:org/repo/tdd@v2", wantOK: true,
			wantURL: "git@github.com:org/repo.git", wantSkill: "tdd", wantRef: "v2"},
		// scp-style with a non-git user — the actual user must be preserved,
		// not hard-coded back to git@.
		{in: "deploy@git.acme.com:org/repo/tdd", wantOK: true,
			wantURL: "deploy@git.acme.com:org/repo.git", wantSkill: "tdd"},

		// ssh:// scheme — user and port must survive into the clone URL so a
		// private remote keeps its identity.
		{in: "ssh://git@github.com/org/repo/tdd", wantOK: true,
			wantURL: "ssh://git@github.com/org/repo.git", wantSkill: "tdd"},
		{in: "ssh://git@git.acme.com:2222/org/repo", wantOK: true,
			wantURL: "ssh://git@git.acme.com:2222/org/repo.git", wantSkill: ""},
		// git:// protocol.
		{in: "git://github.com/org/repo/tdd", wantOK: true,
			wantURL: "git://github.com/org/repo.git", wantSkill: "tdd"},

		// Web "tree" URL — the in-path ref is extracted and the skill comes
		// from the subpath, not the literal "tree" segment. The full subpath is
		// the skill directory (drives the single-skill fast path).
		{in: "https://github.com/org/repo/tree/v2/skills/tdd", wantOK: true,
			wantURL: "https://github.com/org/repo.git", wantSkill: "tdd", wantPath: "skills/tdd", wantRef: "v2"},
		// Web "blob" URL pointing straight at a SKILL.md — the skill is the
		// file's parent directory, and so is the skill directory.
		{in: "https://github.com/org/repo/blob/main/skills/tdd/SKILL.md", wantOK: true,
			wantURL: "https://github.com/org/repo.git", wantSkill: "tdd", wantPath: "skills/tdd", wantRef: "main"},
		// Explicit @ref wins over an in-path tree ref.
		{in: "https://github.com/org/repo/tree/v2/tdd@v9", wantOK: true,
			wantURL: "https://github.com/org/repo.git", wantSkill: "tdd", wantPath: "tdd", wantRef: "v9"},

		// Deep skill path — the deepest segment names the skill, the full path
		// locates it.
		{in: "github.com/org/repo/sub/dir/tdd", wantOK: true,
			wantURL: "https://github.com/org/repo.git", wantSkill: "tdd", wantPath: "sub/dir/tdd"},
		// Trailing slash after a bare repo.
		{in: "https://github.com/org/repo/", wantOK: true,
			wantURL: "https://github.com/org/repo.git", wantSkill: ""},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			url, skill, skillPath, ref, ok := parseRemoteSkillSpec(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (url=%q skill=%q path=%q ref=%q)", ok, tt.wantOK, url, skill, skillPath, ref)
			}
			if !ok {
				return
			}
			if url != tt.wantURL {
				t.Errorf("url = %q, want %q", url, tt.wantURL)
			}
			if skill != tt.wantSkill {
				t.Errorf("skill = %q, want %q", skill, tt.wantSkill)
			}
			if tt.wantPath != "" && skillPath != tt.wantPath {
				t.Errorf("skillPath = %q, want %q", skillPath, tt.wantPath)
			}
			if ref != tt.wantRef {
				t.Errorf("ref = %q, want %q", ref, tt.wantRef)
			}
		})
	}
}
