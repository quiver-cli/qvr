package discover

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestEnumerate_LayoutsAndFilters exercises the walker over temp trees shaped
// like real agent stores: nested date dirs, prefix/suffix matching,
// exclude-suffix rules, and non-recursive stores.
func TestEnumerate_LayoutsAndFilters(t *testing.T) {
	cases := []struct {
		name  string
		store SessionStore
		files []string // relative to root
		want  []string // expected matches (relative)
	}{
		{
			name:  "nested rollouts with prefix",
			store: SessionStore{Recursive: true, NamePrefix: "rollout-", NameSuffixes: []string{".jsonl"}},
			files: []string{
				"2026/06/02/rollout-2026-06-02-abc.jsonl",
				"2026/06/02/notes.txt",
				"2026/06/03/other.jsonl", // missing prefix
			},
			want: []string{"2026/06/02/rollout-2026-06-02-abc.jsonl"},
		},
		{
			name:  "project slug dirs",
			store: SessionStore{Recursive: true, NameSuffixes: []string{".jsonl"}},
			files: []string{
				"-Users-x-proj/aaaa.jsonl",
				"-Users-x-proj/bbbb.jsonl",
				"-Users-x-proj/cache.json", // wrong suffix
			},
			want: []string{"-Users-x-proj/aaaa.jsonl", "-Users-x-proj/bbbb.jsonl"},
		},
		{
			name: "exclude suffixes win",
			store: SessionStore{Recursive: true, NameSuffixes: []string{".jsonl"},
				ExcludeSuffixes: []string{".trajectory.jsonl", ".jsonl.lock"}},
			files: []string{
				"agent1/sessions/s1.jsonl",
				"agent1/sessions/s1.trajectory.jsonl",
				"agent1/sessions/s1.jsonl.lock",
			},
			want: []string{"agent1/sessions/s1.jsonl"},
		},
		{
			name:  "non-recursive stays at root",
			store: SessionStore{Recursive: false, NamePrefix: "session_", NameSuffixes: []string{".json"}},
			files: []string{
				"session_1.json",
				"nested/session_2.json", // below root — must not match
			},
			want: []string{"session_1.json"},
		},
		{
			name:  "multiple suffixes",
			store: SessionStore{Recursive: true, NamePrefix: "session-", NameSuffixes: []string{".json", ".jsonl"}},
			files: []string{
				"hash1/chats/session-1.json",
				"hash1/chats/session-2.jsonl",
				"hash1/chats/checkpoint.bin",
			},
			want: []string{"hash1/chats/session-1.json", "hash1/chats/session-2.jsonl"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Canonicalize: the walker resolves symlinked roots (macOS tmp dirs
			// live under the /var → /private/var symlink), so candidate paths
			// come back canonical.
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range tc.files {
				writeFile(t, filepath.Join(root, f), "{}\n")
			}
			st := tc.store
			st.Roots = []string{root}
			got, werr := enumerate(st, time.Time{})
			if werr != nil {
				t.Fatalf("enumerate: %v", werr)
			}
			found := map[string]bool{}
			for _, c := range got {
				rel, _ := filepath.Rel(root, c.path)
				found[rel] = true
				if c.size == 0 || c.mtimeMs == 0 {
					t.Errorf("%s: missing stat fingerprint: %+v", rel, c)
				}
			}
			if len(found) != len(tc.want) {
				t.Fatalf("matches = %v, want %v", found, tc.want)
			}
			for _, w := range tc.want {
				if !found[w] {
					t.Errorf("missing expected match %s", w)
				}
			}
		})
	}
}

// TestEnumerate_MissingRootIsEmpty pins: an agent that isn't installed (no
// store dir) contributes nothing and no error.
func TestEnumerate_MissingRootIsEmpty(t *testing.T) {
	st := SessionStore{Roots: []string{filepath.Join(t.TempDir(), "nope")}, Recursive: true, NameSuffixes: []string{".jsonl"}}
	got, err := enumerate(st, time.Time{})
	if err != nil || len(got) != 0 {
		t.Errorf("missing root: got %d candidates, err %v; want 0, nil", len(got), err)
	}
}

// TestEnumerate_SinceCutoff pins the mtime filter.
func TestEnumerate_SinceCutoff(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "old.jsonl")
	fresh := filepath.Join(root, "fresh.jsonl")
	writeFile(t, old, "{}\n")
	writeFile(t, fresh, "{}\n")
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}

	st := SessionStore{Roots: []string{root}, Recursive: true, NameSuffixes: []string{".jsonl"}}
	got, err := enumerate(st, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	if len(got) != 1 || filepath.Base(got[0].path) != "fresh.jsonl" {
		t.Errorf("since cutoff: got %+v, want only fresh.jsonl", got)
	}
}

// TestEnumerate_EnvRootOverride pins the env-var base override.
func TestEnumerate_EnvRootOverride(t *testing.T) {
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "sessions", "s1.jsonl"), "{}\n")
	t.Setenv("QVR_TEST_STORE_HOME", base)

	st := SessionStore{
		Roots:         []string{filepath.Join(t.TempDir(), "absent")},
		EnvRoot:       "QVR_TEST_STORE_HOME",
		EnvRootSubdir: "sessions",
		Recursive:     true,
		NameSuffixes:  []string{".jsonl"},
	}
	got, err := enumerate(st, time.Time{})
	if err != nil || len(got) != 1 {
		t.Errorf("env root: got %d candidates, err %v; want 1, nil", len(got), err)
	}
}

// TestScannable_RequiresDeriver pins the activation rule: only deriver-backed
// agents scan; the rest are inert table rows.
func TestScannable_RequiresDeriver(t *testing.T) {
	active := map[string]bool{}
	for _, st := range Scannable(nil) {
		active[st.Agent] = true
	}
	for _, want := range []string{"claude", "codex", "openclaw", "pi", "copilot", "droid", "cursor", "gemini", "hermes", "opencode"} {
		if !active[want] {
			t.Errorf("%s should be scannable (deriver registered)", want)
		}
	}
	if got := Scannable([]string{"codex"}); len(got) != 1 || got[0].Agent != "codex" {
		t.Errorf("agent restriction: got %+v, want codex only", got)
	}
}
