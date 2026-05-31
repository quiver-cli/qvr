package manifest_test

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/raks097/quiver/internal/manifest"
)

func TestParse_BasicThreeColumn(t *testing.T) {
	in := `# qvr export — 2026-05-31
https://github.com/raks097/quiver_playground.git  code-review        v0.2.0
https://github.com/vercel-labs/agent-skills.git   deploy-to-vercel   main
https://github.com/OmidZamani/dspy-skills.git     dspy-rag-pipeline  master
`
	got, perrs, err := manifest.Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(perrs) != 0 {
		t.Fatalf("unexpected parse errors: %v", perrs)
	}
	want := []manifest.Entry{
		{RepoURL: "https://github.com/raks097/quiver_playground.git", Skill: "code-review", Version: "v0.2.0", Line: 2},
		{RepoURL: "https://github.com/vercel-labs/agent-skills.git", Skill: "deploy-to-vercel", Version: "main", Line: 3},
		{RepoURL: "https://github.com/OmidZamani/dspy-skills.git", Skill: "dspy-rag-pipeline", Version: "master", Line: 4},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entries mismatch:\n got = %#v\nwant = %#v", got, want)
	}
}

func TestParse_AllFlags(t *testing.T) {
	in := `https://github.com/raks097/quiver_playground.git  code-review  v0.2.0  --commit=94e539be7d6a01774d723a7c25513af0f070de7b  --target=claude,cursor  --as=cr-v2  --registry-alias=raks
`
	got, perrs, err := manifest.Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(perrs) != 0 {
		t.Fatalf("unexpected parse errors: %v", perrs)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	e := got[0]
	if e.Commit != "94e539be7d6a01774d723a7c25513af0f070de7b" {
		t.Errorf("commit: got %q", e.Commit)
	}
	if !reflect.DeepEqual(e.Targets, []string{"claude", "cursor"}) {
		t.Errorf("targets: got %v", e.Targets)
	}
	if e.Alias != "cr-v2" {
		t.Errorf("alias: got %q", e.Alias)
	}
	if e.RegistryAlias != "raks" {
		t.Errorf("registry-alias: got %q", e.RegistryAlias)
	}
}

func TestParse_CommentsAndBlanks(t *testing.T) {
	in := `# leading comment
# another
   # indented comment is still a comment

https://example.com/r.git  s  v1   # trailing comment
`
	got, perrs, err := manifest.Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(perrs) != 0 {
		t.Fatalf("unexpected parse errors: %v", perrs)
	}
	if len(got) != 1 || got[0].Skill != "s" || got[0].Version != "v1" {
		t.Fatalf("unexpected entries: %#v", got)
	}
}

func TestParse_Errors(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{"too few fields", "https://x.git  s", "expected at least 3 fields"},
		{"unknown flag", "https://x.git  s  v1  --weird=1", "unknown flag --weird"},
		{"flag without value", "https://x.git  s  v1  --commit=", "empty value"},
		{"positional after flags", "https://x.git  s  v1  --commit=abc  oops", "unexpected positional"},
		{"flag without =", "https://x.git  s  v1  --commit", "must be --key=value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, perrs, err := manifest.Parse(strings.NewReader(tc.line + "\n"))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(perrs) != 1 {
				t.Fatalf("expected 1 parse error, got %d: %v", len(perrs), perrs)
			}
			if !strings.Contains(perrs[0].Msg, tc.want) {
				t.Fatalf("error msg = %q, want substring %q", perrs[0].Msg, tc.want)
			}
		})
	}
}

func TestFormat_RoundTrip(t *testing.T) {
	entries := []manifest.Entry{
		{RepoURL: "https://github.com/raks097/quiver_playground.git", Skill: "code-review", Version: "v0.2.0"},
		{RepoURL: "https://github.com/vercel-labs/agent-skills.git", Skill: "deploy-to-vercel", Version: "main", Targets: []string{"cursor", "claude"}},
		{RepoURL: "https://github.com/OmidZamani/dspy-skills.git", Skill: "dspy-rag-pipeline", Version: "master", Commit: "deadbeef", Alias: "dspy", RegistryAlias: "omid"},
	}
	var buf bytes.Buffer
	if err := manifest.Format(&buf, manifest.FormatOptions{Header: "qvr export\n", Align: true}, entries); err != nil {
		t.Fatalf("Format: %v", err)
	}

	got, perrs, err := manifest.Parse(&buf)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(perrs) != 0 {
		t.Fatalf("unexpected parse errors: %v", perrs)
	}
	// Line numbers depend on the header; compare everything else.
	for i := range got {
		got[i].Line = 0
	}
	want := []manifest.Entry{
		{RepoURL: "https://github.com/raks097/quiver_playground.git", Skill: "code-review", Version: "v0.2.0"},
		// Targets are sorted on emit so the round-trip lands the slice sorted.
		{RepoURL: "https://github.com/vercel-labs/agent-skills.git", Skill: "deploy-to-vercel", Version: "main", Targets: []string{"claude", "cursor"}},
		{RepoURL: "https://github.com/OmidZamani/dspy-skills.git", Skill: "dspy-rag-pipeline", Version: "master", Commit: "deadbeef", Alias: "dspy", RegistryAlias: "omid"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip mismatch:\n got = %#v\nwant = %#v", got, want)
	}
}

func TestFormat_DeterministicFlagOrder(t *testing.T) {
	entry := manifest.Entry{
		RepoURL:       "https://x.git",
		Skill:         "s",
		Version:       "v1",
		Commit:        "abc1234",
		Targets:       []string{"claude"},
		Alias:         "s2",
		RegistryAlias: "raks",
	}
	var a, b bytes.Buffer
	_ = manifest.Format(&a, manifest.FormatOptions{}, []manifest.Entry{entry})
	_ = manifest.Format(&b, manifest.FormatOptions{}, []manifest.Entry{entry})
	if a.String() != b.String() {
		t.Fatalf("non-deterministic format:\n a = %q\n b = %q", a.String(), b.String())
	}
	if !strings.Contains(a.String(), "--commit=abc1234  --target=claude  --as=s2  --registry-alias=raks") {
		t.Fatalf("flag order or shape unexpected: %q", a.String())
	}
}
