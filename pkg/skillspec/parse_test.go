package skillspec_test

import (
	"strings"
	"testing"

	"github.com/astra-sh/qvr/pkg/skillspec"
)

func TestParse_ValidFull(t *testing.T) {
	content := `---
name: test-skill
description: A test skill for validation.
license: MIT
compatibility: Requires git
metadata:
  author: test-org
  version: "1.0"
allowed-tools: Bash(git:*) Read
---

# Test Skill

Instructions here.
`
	s, err := skillspec.Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if s.Frontmatter.Name != "test-skill" {
		t.Errorf("name = %q, want %q", s.Frontmatter.Name, "test-skill")
	}
	if s.Frontmatter.Description != "A test skill for validation." {
		t.Errorf("description = %q, want %q", s.Frontmatter.Description, "A test skill for validation.")
	}
	if s.Frontmatter.License != "MIT" {
		t.Errorf("license = %q, want %q", s.Frontmatter.License, "MIT")
	}
	if s.Frontmatter.Compatibility != "Requires git" {
		t.Errorf("compatibility = %q, want %q", s.Frontmatter.Compatibility, "Requires git")
	}
	if s.Frontmatter.Metadata["author"] != "test-org" {
		t.Errorf("metadata.author = %q, want %q", s.Frontmatter.Metadata["author"], "test-org")
	}
	if s.Frontmatter.AllowedTools != "Bash(git:*) Read" {
		t.Errorf("allowed-tools = %q, want %q", s.Frontmatter.AllowedTools, "Bash(git:*) Read")
	}
	if s.Body != "# Test Skill\n\nInstructions here." {
		t.Errorf("body = %q", s.Body)
	}
	if s.Raw != content {
		t.Error("raw should equal original content")
	}
}

func TestParse_MinimalValid(t *testing.T) {
	content := `---
name: minimal
description: Minimal skill.
---

Body.
`
	s, err := skillspec.Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Frontmatter.Name != "minimal" {
		t.Errorf("name = %q", s.Frontmatter.Name)
	}
	if s.Frontmatter.License != "" {
		t.Errorf("license should be empty, got %q", s.Frontmatter.License)
	}
}

func TestParse_EmptyContent(t *testing.T) {
	_, err := skillspec.Parse("")
	if err != skillspec.ErrEmptyContent {
		t.Errorf("err = %v, want ErrEmptyContent", err)
	}
}

func TestParse_WhitespaceOnly(t *testing.T) {
	_, err := skillspec.Parse("   \n\n  \t  ")
	if err != skillspec.ErrEmptyContent {
		t.Errorf("err = %v, want ErrEmptyContent", err)
	}
}

func TestParse_NoFrontmatter(t *testing.T) {
	_, err := skillspec.Parse("Just some text without frontmatter.")
	if err != skillspec.ErrNoFrontmatter {
		t.Errorf("err = %v, want ErrNoFrontmatter", err)
	}
}

func TestParse_UnclosedFrontmatter(t *testing.T) {
	content := `---
name: unclosed
description: Missing closing delimiter.
`
	_, err := skillspec.Parse(content)
	if err != skillspec.ErrMalformedContent {
		t.Errorf("err = %v, want ErrMalformedContent", err)
	}
}

func TestParse_InvalidYAML(t *testing.T) {
	content := `---
name: [invalid yaml
  bad: {structure
---

Body.
`
	_, err := skillspec.Parse(content)
	if err != skillspec.ErrInvalidYAML {
		t.Errorf("err = %v, want ErrInvalidYAML", err)
	}
}

func TestParse_EmptyBody(t *testing.T) {
	content := `---
name: nobody
description: Skill with no body content.
---
`
	s, err := skillspec.Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Body != "" {
		t.Errorf("body should be empty, got %q", s.Body)
	}
}

func TestParse_MultilineDescription(t *testing.T) {
	content := `---
name: multiline
description: >
  This is a multiline
  description that spans
  multiple lines.
---

Body.
`
	s, err := skillspec.Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Frontmatter.Name != "multiline" {
		t.Errorf("name = %q", s.Frontmatter.Name)
	}
	if s.Frontmatter.Description == "" {
		t.Error("description should not be empty for multiline")
	}
	// YAML folded scalars append a trailing newline; parser must trim it so
	// downstream renderers don't carry stray whitespace.
	if got := s.Frontmatter.Description; got != "This is a multiline description that spans multiple lines." {
		t.Errorf("description = %q, want trimmed folded scalar", got)
	}
}

func TestParse_BlockScalarDescription(t *testing.T) {
	content := `---
name: block
description: |
  Line one.
  Line two.
---

Body.
`
	s, err := skillspec.Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := s.Frontmatter.Description; got != "Line one.\nLine two." {
		t.Errorf("description = %q, want trimmed block scalar", got)
	}
}

// Several upstream registries (claude-plugin marketplaces, the dspy-skills
// repo) ship `allowed-tools` as a YAML sequence rather than the spec's
// space-delimited string. The parser flattens both forms to the canonical
// string so downstream consumers don't have to care which form was on disk.
func TestParse_AllowedToolsArrayForm(t *testing.T) {
	content := `---
name: array-tools
description: Allowed tools as a YAML array.
allowed-tools:
  - Read
  - Write
  - Glob
  - Grep
---

Body.
`
	s, err := skillspec.Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := s.Frontmatter.AllowedTools, "Read Write Glob Grep"; got != want {
		t.Errorf("allowed-tools = %q, want %q", got, want)
	}
}

func TestParse_AllowedToolsScalarFormUnchanged(t *testing.T) {
	content := `---
name: scalar-tools
description: Spec-canonical space-delimited form.
allowed-tools: Bash(git:*) Read
---

Body.
`
	s, err := skillspec.Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := s.Frontmatter.AllowedTools, "Bash(git:*) Read"; got != want {
		t.Errorf("allowed-tools = %q, want %q", got, want)
	}
}

func TestParse_AllowedToolsEmptyArray(t *testing.T) {
	content := `---
name: empty-array
description: Empty array should flatten to empty string.
allowed-tools: []
---

Body.
`
	s, err := skillspec.Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Frontmatter.AllowedTools != "" {
		t.Errorf("expected empty allowed-tools, got %q", s.Frontmatter.AllowedTools)
	}
}

func TestParse_AllowedToolsRejectsMapping(t *testing.T) {
	content := `---
name: bad-tools
description: Mapping form is not supported.
allowed-tools:
  read: true
  write: false
---

Body.
`
	_, err := skillspec.Parse(content)
	if err == nil {
		t.Fatal("expected error for mapping-form allowed-tools, got nil")
	}
}

// TestParse_UnquotedColonInDescription verifies the lenient fallback that
// accepts SKILL.md files whose author wrote `description: TL;DR: foo`
// without realizing YAML's colon-in-scalar rule. Strict YAML rejects these,
// but they're common enough in shipped skills (acme/skills, others)
// that we auto-quote on the second pass.
func TestParse_UnquotedColonInDescription(t *testing.T) {
	content := `---
name: x-article-editor
description: TL;DR: Turn a topic into a high-engagement article. STEP 1: write. STEP 2: review.
---

Body.
`
	s, err := skillspec.Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Frontmatter.Name != "x-article-editor" {
		t.Errorf("name = %q", s.Frontmatter.Name)
	}
	expected := "TL;DR: Turn a topic into a high-engagement article. STEP 1: write. STEP 2: review."
	if s.Frontmatter.Description != expected {
		t.Errorf("description = %q, want %q", s.Frontmatter.Description, expected)
	}
}

// TestParse_LenientFallbackPreservesQuoted: when a value is already quoted,
// the lenient pass leaves it alone so we don't double-wrap or break escaping.
func TestParse_LenientFallbackPreservesQuoted(t *testing.T) {
	content := `---
name: ok
description: "Already: quoted"
metadata:
  note: "user: pinned"
---

Body.
`
	s, err := skillspec.Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Frontmatter.Description != "Already: quoted" {
		t.Errorf("description = %q", s.Frontmatter.Description)
	}
	if s.Frontmatter.Metadata["note"] != "user: pinned" {
		t.Errorf("metadata.note = %q", s.Frontmatter.Metadata["note"])
	}
}

func TestParse_NestedMetadata(t *testing.T) {
	content := `---
name: tweetclaw
description: X/Twitter automation workflows.
metadata: {"openclaw":{"tags":["twitter","x","tweet-scraper"],"primaryEnv":"XQUIK_API_KEY","envVars":[{"name":"XQUIK_API_KEY","required":false}]}}
---

Body.
`
	s, err := skillspec.Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := s.Frontmatter.Metadata["openclaw"]
	for _, want := range []string{
		`"primaryEnv":"XQUIK_API_KEY"`,
		`"tags":["twitter","x","tweet-scraper"]`,
		`"envVars":[{"name":"XQUIK_API_KEY","required":false}]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("metadata.openclaw = %q, want substring %q", got, want)
		}
	}
}

func TestParse_NullMetadata(t *testing.T) {
	content := `---
name: no-metadata
description: Explicit null metadata is absent.
metadata: null
---

Body.
`
	s, err := skillspec.Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Frontmatter.Metadata != nil {
		t.Errorf("metadata = %#v, want nil", s.Frontmatter.Metadata)
	}
}

func TestParse_MetadataAliases(t *testing.T) {
	content := `---
name: aliased-metadata
description: Metadata values may use YAML anchors.
metadata:
  tags: &tags
    - twitter
    - x
  searchTags: *tags
  config: &config
    primaryEnv: XQUIK_API_KEY
  openclaw: *config
---

Body.
`
	s, err := skillspec.Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := s.Frontmatter.Metadata["searchTags"]; got != `["twitter","x"]` {
		t.Errorf("metadata.searchTags = %q", got)
	}
	if got := s.Frontmatter.Metadata["openclaw"]; got != `{"primaryEnv":"XQUIK_API_KEY"}` {
		t.Errorf("metadata.openclaw = %q", got)
	}
}

func TestParse_NonMappingMetadataRejected(t *testing.T) {
	content := `---
name: bad-metadata
description: Metadata must be a mapping.
metadata:
  - author
---

Body.
`
	_, err := skillspec.Parse(content)
	if err == nil {
		t.Fatal("expected error for sequence metadata, got nil")
	}
}

func TestParse_LeadingWhitespace(t *testing.T) {
	content := `

---
name: leading-ws
description: Has leading whitespace.
---

Body.
`
	s, err := skillspec.Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Frontmatter.Name != "leading-ws" {
		t.Errorf("name = %q", s.Frontmatter.Name)
	}
}
