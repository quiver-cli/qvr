package registry_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/quiver-cli/qvr/internal/config"
	"github.com/quiver-cli/qvr/internal/git"
	"github.com/quiver-cli/qvr/internal/registry"
)

// mockGitClient implements git.GitClient for testing.
type mockGitClient struct {
	trees         map[string][]git.TreeEntry // key: "ref:path"
	blobs         map[string][]byte          // key: "ref:filepath"
	branches      []git.RefInfo
	tags          []git.RefInfo
	defaultBranch string
	headCommit    string
}

func newMockGitClient() *mockGitClient {
	return &mockGitClient{
		trees:         make(map[string][]git.TreeEntry),
		blobs:         make(map[string][]byte),
		defaultBranch: "main",
		headCommit:    "abc123",
	}
}

func (m *mockGitClient) BareClone(ctx context.Context, url, path string, opts git.CloneOptions) error {
	return nil
}
func (m *mockGitClient) Clone(ctx context.Context, url, path string) error { return nil }
func (m *mockGitClient) SubdirClone(ctx context.Context, url, ref, subpath, dest string) error {
	return nil
}
func (m *mockGitClient) Fetch(ctx context.Context, repoPath string) error        { return nil }
func (m *mockGitClient) DeepenToFull(ctx context.Context, repoPath string) error { return nil }
func (m *mockGitClient) FetchWorktree(ctx context.Context, worktreePath string) error {
	return nil
}
func (m *mockGitClient) Push(ctx context.Context, repoPath, remote string, refSpecs []string) error {
	return nil
}

func (m *mockGitClient) ListBranches(repoPath string) ([]git.RefInfo, error) {
	return m.branches, nil
}

func (m *mockGitClient) ListTags(repoPath string) ([]git.RefInfo, error) {
	return m.tags, nil
}

func (m *mockGitClient) LsRemote(ctx context.Context, url string) (*git.RemoteRefInfo, error) {
	return &git.RemoteRefInfo{Refs: map[string]string{}}, nil
}

func (m *mockGitClient) RemoteDefaultBranch(ctx context.Context, url string) (string, error) {
	return m.defaultBranch, nil
}

func (m *mockGitClient) HeadCommit(repoPath string) (string, error) {
	return m.headCommit, nil
}

func (m *mockGitClient) ResolveRef(repoPath, ref string) (string, error) {
	return m.headCommit, nil
}

func (m *mockGitClient) DefaultBranch(repoPath string) (string, error) {
	return m.defaultBranch, nil
}

func (m *mockGitClient) ReadBlob(repoPath, ref, filePath string) ([]byte, error) {
	key := ref + ":" + filePath
	data, ok := m.blobs[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", git.ErrBlobNotFound, filePath)
	}
	return data, nil
}

func (m *mockGitClient) ListTree(repoPath, ref, path string) ([]git.TreeEntry, error) {
	key := ref + ":" + path
	entries, ok := m.trees[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", git.ErrTreeNotFound, path)
	}
	return entries, nil
}

// ListBlobsRecursive derives the recursive blob list from the configured
// blobs map (keyed "ref:filepath"), filtering to those under `path`. This
// mirrors the real client: every file the test registers is discoverable.
func (m *mockGitClient) ListBlobsRecursive(repoPath, ref, path string) ([]git.TreeEntry, error) {
	prefix := ref + ":"
	var subdir string
	if path != "" {
		subdir = strings.TrimSuffix(path, "/") + "/"
	}
	var out []git.TreeEntry
	for key := range m.blobs {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		fp := key[len(prefix):]
		if subdir != "" && !strings.HasPrefix(fp, subdir) {
			continue
		}
		name := fp
		if i := strings.LastIndex(fp, "/"); i >= 0 {
			name = fp[i+1:]
		}
		out = append(out, git.TreeEntry{Name: name, Path: fp})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func TestBuildIndex_SkillsDir(t *testing.T) {
	mock := newMockGitClient()
	mock.trees["HEAD:skills"] = []git.TreeEntry{
		{Name: "code-review", Path: "skills/code-review", IsDir: true},
		{Name: "deploy-helper", Path: "skills/deploy-helper", IsDir: true},
	}
	mock.blobs["HEAD:skills/code-review/SKILL.md"] = []byte(`---
name: code-review
description: Reviews pull requests.
metadata:
  author: acme
---
# Code Review
`)
	mock.blobs["HEAD:skills/deploy-helper/SKILL.md"] = []byte(`---
name: deploy-helper
description: Helps with deployments.
---
# Deploy Helper
`)
	mock.branches = []git.RefInfo{{Name: "main", Hash: "aaa"}}
	mock.tags = []git.RefInfo{{Name: "v1.0.0", Hash: "bbb", IsTag: true}}

	indexer := registry.NewIndexer(mock)
	skills, skipped, err := indexer.BuildIndex("/fake/path")
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}

	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(skills))
	}
	if len(skipped) != 0 {
		t.Errorf("expected 0 skipped, got %d: %+v", len(skipped), skipped)
	}

	names := map[string]bool{}
	for _, s := range skills {
		names[s.Name] = true
		if len(s.Versions.Branches) != 1 || s.Versions.Branches[0] != "main" {
			t.Errorf("expected branches [main], got %v", s.Versions.Branches)
		}
		if len(s.Versions.Tags) != 1 || s.Versions.Tags[0] != "v1.0.0" {
			t.Errorf("expected tags [v1.0.0], got %v", s.Versions.Tags)
		}
	}
	if !names["code-review"] || !names["deploy-helper"] {
		t.Errorf("expected code-review and deploy-helper, got %v", names)
	}
}

func TestBuildIndex_RootSkill(t *testing.T) {
	mock := newMockGitClient()
	// No skills/ directory
	mock.blobs["HEAD:SKILL.md"] = []byte(`---
name: my-skill
description: A standalone skill.
---
# My Skill
`)

	indexer := registry.NewIndexer(mock)
	skills, _, err := indexer.BuildIndex("/fake/path")
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}

	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Name != "my-skill" {
		t.Errorf("expected my-skill, got %s", skills[0].Name)
	}
	if skills[0].Path != "." {
		t.Errorf("expected path '.', got %s", skills[0].Path)
	}
}

func TestBuildIndex_EmptyRegistry(t *testing.T) {
	mock := newMockGitClient()
	// No skills/ dir, no root SKILL.md

	indexer := registry.NewIndexer(mock)
	skills, skipped, err := indexer.BuildIndex("/fake/path")
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}

	if len(skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(skills))
	}
	if len(skipped) != 0 {
		t.Errorf("expected 0 skipped, got %d", len(skipped))
	}
}

func TestBuildIndex_InvalidSkillMDSkipped(t *testing.T) {
	mock := newMockGitClient()
	mock.trees["HEAD:skills"] = []git.TreeEntry{
		{Name: "valid", Path: "skills/valid", IsDir: true},
		{Name: "invalid", Path: "skills/invalid", IsDir: true},
	}
	mock.blobs["HEAD:skills/valid/SKILL.md"] = []byte(`---
name: valid
description: A valid skill.
---
`)
	mock.blobs["HEAD:skills/invalid/SKILL.md"] = []byte(`not valid frontmatter`)

	indexer := registry.NewIndexer(mock)
	skills, skipped, err := indexer.BuildIndex("/fake/path")
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}

	if len(skills) != 1 {
		t.Fatalf("expected 1 skill (invalid skipped), got %d", len(skills))
	}
	if skills[0].Name != "valid" {
		t.Errorf("expected valid, got %s", skills[0].Name)
	}
	if len(skipped) != 1 {
		t.Fatalf("expected 1 skipped entry, got %d: %+v", len(skipped), skipped)
	}
	if skipped[0].Name != "invalid" {
		t.Errorf("skipped[0].Name = %q, want %q", skipped[0].Name, "invalid")
	}
	if skipped[0].Path != "skills/invalid/SKILL.md" {
		t.Errorf("skipped[0].Path = %q, want skills/invalid/SKILL.md", skipped[0].Path)
	}
	if skipped[0].Reason == "" {
		t.Error("skipped entry should carry a non-empty reason")
	}
}

func TestBuildIndex_FileEntriesSkipped(t *testing.T) {
	mock := newMockGitClient()
	mock.trees["HEAD:skills"] = []git.TreeEntry{
		{Name: "README.md", Path: "skills/README.md", IsDir: false},
		{Name: "my-skill", Path: "skills/my-skill", IsDir: true},
	}
	mock.blobs["HEAD:skills/my-skill/SKILL.md"] = []byte(`---
name: my-skill
description: A skill.
---
`)

	indexer := registry.NewIndexer(mock)
	skills, skipped, err := indexer.BuildIndex("/fake/path")
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}

	if len(skills) != 1 {
		t.Errorf("expected 1 skill (README.md file skipped), got %d", len(skills))
	}
	// README.md is a file, not a candidate skill — it should be filtered out
	// silently, not surfaced as a "skipped" entry.
	if len(skipped) != 0 {
		t.Errorf("non-directory entries should not be reported as skipped, got %+v", skipped)
	}
}

func TestBuildIndex_NoSkillMDDirInvisible(t *testing.T) {
	mock := newMockGitClient()
	mock.blobs["HEAD:skills/has-skill/SKILL.md"] = []byte(`---
name: has-skill
description: Has a SKILL.md.
---
`)
	// no-skill is a directory with files but no SKILL.md.
	mock.blobs["HEAD:skills/no-skill/README.md"] = []byte("not a skill")

	indexer := registry.NewIndexer(mock)
	skills, skipped, err := indexer.BuildIndex("/fake/path")
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}

	// Under recursive discovery a directory without a SKILL.md is not a skill
	// candidate at all — it is invisible, not "skipped" (consistent with how
	// non-SKILL.md files are treated).
	if len(skills) != 1 || skills[0].Name != "has-skill" {
		t.Errorf("expected only has-skill, got %+v", skills)
	}
	if len(skipped) != 0 {
		t.Errorf("a dir without SKILL.md should not be reported as skipped, got %+v", skipped)
	}
}

// TestBuildIndex_AllowedToolsArrayAccepted verifies the regression that broke
// the dspy-skills registry: skills shipped with `allowed-tools` as a YAML
// sequence (the form claude-plugin marketplaces emit) used to fail YAML
// unmarshalling and end up silently dropped from the index.
func TestBuildIndex_AllowedToolsArrayAccepted(t *testing.T) {
	mock := newMockGitClient()
	mock.trees["HEAD:skills"] = []git.TreeEntry{
		{Name: "array-tools", Path: "skills/array-tools", IsDir: true},
	}
	mock.blobs["HEAD:skills/array-tools/SKILL.md"] = []byte(`---
name: array-tools
description: Skill with allowed-tools as a YAML array.
allowed-tools:
  - Read
  - Write
  - Glob
---
# Body
`)

	indexer := registry.NewIndexer(mock)
	skills, skipped, err := indexer.BuildIndex("/fake/path")
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill (array allowed-tools should parse), got %d skills, skipped=%+v", len(skills), skipped)
	}
	if skills[0].Name != "array-tools" {
		t.Errorf("name = %q, want array-tools", skills[0].Name)
	}
	if len(skipped) != 0 {
		t.Errorf("array form should not be skipped, got %+v", skipped)
	}
}

// TestBuildIndex_RootWithSiblings covers the core #153 layout: a root SKILL.md
// coexisting with several <name>/SKILL.md sibling directories at the repo root.
// All of them must be indexed, and the root entry flagged RootCoexists so the
// scan/install path scopes it to its own content.
func TestBuildIndex_RootWithSiblings(t *testing.T) {
	mock := newMockGitClient()
	mock.blobs["HEAD:SKILL.md"] = []byte("---\nname: root-app\ndescription: The whole-repo skill.\n---\n")
	mock.blobs["HEAD:a/SKILL.md"] = []byte("---\nname: a\ndescription: Sibling a.\n---\n")
	mock.blobs["HEAD:b/SKILL.md"] = []byte("---\nname: b\ndescription: Sibling b.\n---\n")
	mock.blobs["HEAD:c/SKILL.md"] = []byte("---\nname: c\ndescription: Sibling c.\n---\n")
	// App code / fixtures that must NOT be mistaken for skills.
	mock.blobs["HEAD:bin/app.sh"] = []byte("#!/bin/sh\n")
	mock.blobs["HEAD:test/fixtures/creds.env"] = []byte("AKIA...\n")

	skills, skipped, err := registry.NewIndexer(mock).BuildIndex("/fake/path")
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	if len(skipped) != 0 {
		t.Errorf("expected 0 skipped, got %+v", skipped)
	}

	byName := map[string]registry.SkillIndexEntry{}
	for _, s := range skills {
		byName[s.Name] = s
	}
	if len(skills) != 4 {
		t.Fatalf("expected 4 skills (root + a/b/c), got %d: %+v", len(skills), skills)
	}
	if byName["root-app"].Path != "." {
		t.Errorf("root path = %q, want '.'", byName["root-app"].Path)
	}
	if !byName["root-app"].RootCoexists {
		t.Error("root-app should be flagged RootCoexists when siblings exist")
	}
	for _, n := range []string{"a", "b", "c"} {
		if byName[n].Path != n {
			t.Errorf("%s path = %q, want %q", n, byName[n].Path, n)
		}
		if byName[n].RootCoexists {
			t.Errorf("sibling %s should not be flagged RootCoexists", n)
		}
	}
}

// TestBuildIndex_NestedSkillPruned verifies a SKILL.md inside another skill's
// subtree is treated as that skill's asset, not a separate skill.
func TestBuildIndex_NestedSkillPruned(t *testing.T) {
	mock := newMockGitClient()
	mock.blobs["HEAD:a/SKILL.md"] = []byte("---\nname: a\ndescription: Outer skill.\n---\n")
	mock.blobs["HEAD:a/references/SKILL.md"] = []byte("---\nname: example\ndescription: An embedded example, not a skill.\n---\n")

	skills, skipped, err := registry.NewIndexer(mock).BuildIndex("/fake/path")
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	if len(skills) != 1 || skills[0].Name != "a" {
		t.Fatalf("expected only outer skill 'a', got %+v", skills)
	}
	if len(skipped) != 0 {
		t.Errorf("nested SKILL.md is an asset, not a skipped skill, got %+v", skipped)
	}
	if skills[0].RootCoexists {
		t.Error("single non-root skill should not be RootCoexists")
	}
}

// TestBuildIndex_ArbitraryDepth verifies skills are discovered at any depth.
func TestBuildIndex_ArbitraryDepth(t *testing.T) {
	mock := newMockGitClient()
	mock.blobs["HEAD:category/sub/foo/SKILL.md"] = []byte("---\nname: foo\ndescription: Deeply nested.\n---\n")

	skills, _, err := registry.NewIndexer(mock).BuildIndex("/fake/path")
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	if len(skills) != 1 || skills[0].Path != "category/sub/foo" {
		t.Fatalf("expected one skill at category/sub/foo, got %+v", skills)
	}
}

// TestBuildIndex_NameDirMismatchSkipped verifies a non-root skill whose
// frontmatter name disagrees with its directory is surfaced as skipped (it
// could never pass install-time validation).
func TestBuildIndex_NameDirMismatchSkipped(t *testing.T) {
	mock := newMockGitClient()
	mock.blobs["HEAD:browse/SKILL.md"] = []byte("---\nname: not-browse\ndescription: Misnamed.\n---\n")

	skills, skipped, err := registry.NewIndexer(mock).BuildIndex("/fake/path")
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	if len(skills) != 0 {
		t.Errorf("expected 0 indexed skills, got %+v", skills)
	}
	if len(skipped) != 1 || skipped[0].Name != "not-browse" {
		t.Fatalf("expected mismatch skipped, got %+v", skipped)
	}
	if !strings.Contains(skipped[0].Reason, "does not match directory") {
		t.Errorf("reason = %q, want a name/dir mismatch", skipped[0].Reason)
	}
}

// TestBuildIndex_DuplicateNameSkipped verifies two skills resolving to the same
// name keep the first (by sorted path) and skip the rest.
func TestBuildIndex_DuplicateNameSkipped(t *testing.T) {
	mock := newMockGitClient()
	// Same frontmatter name "dup" from two directories. Only "dup" (sorts before
	// "z-dup") can match its own dir, so use a layout where both can be valid:
	// skills/dup and dup both have name "dup".
	mock.blobs["HEAD:dup/SKILL.md"] = []byte("---\nname: dup\ndescription: First.\n---\n")
	mock.blobs["HEAD:skills/dup/SKILL.md"] = []byte("---\nname: dup\ndescription: Second.\n---\n")

	skills, skipped, err := registry.NewIndexer(mock).BuildIndex("/fake/path")
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	if len(skills) != 1 || skills[0].Path != "dup" {
		t.Fatalf("expected first-by-sorted-path 'dup' kept, got %+v", skills)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0].Reason, "duplicate skill name") {
		t.Fatalf("expected duplicate skipped, got %+v", skipped)
	}
}

// TestSkillScopePaths covers the scope helper that scan + install both use.
func TestSkillScopePaths(t *testing.T) {
	if got := registry.SkillScopePaths(registry.SkillIndexEntry{Path: "browse"}); len(got) != 1 || got[0] != "browse" {
		t.Errorf("non-root scope = %v, want [browse]", got)
	}
	if got := registry.SkillScopePaths(registry.SkillIndexEntry{Path: "."}); got != nil {
		t.Errorf("lone root scope = %v, want nil (whole repo)", got)
	}
	got := registry.SkillScopePaths(registry.SkillIndexEntry{Path: ".", RootCoexists: true})
	want := map[string]bool{"SKILL.md": true, "references": true, "scripts": true, "assets": true}
	if len(got) != len(want) {
		t.Fatalf("root-coexists scope = %v, want the 4 content patterns", got)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected scope path %q", p)
		}
	}
}

func TestSubstringSearch(t *testing.T) {
	entries := []registry.SkillIndexEntry{
		{Name: "code-review", Description: "Reviews code in pull requests.", Metadata: map[string]string{"author": "acme"}},
		{Name: "deploy-helper", Description: "Helps deploy applications.", Metadata: map[string]string{"author": "acme"}},
		{Name: "review-bot", Description: "Automated review bot.", Metadata: nil},
	}

	tests := []struct {
		query     string
		wantCount int
		wantFirst string
	}{
		{"review", 2, "code-review"}, // name match (3.0) + desc match (1.0) = 4.0 vs desc match (1.0)
		{"deploy", 1, "deploy-helper"},
		{"acme", 2, ""}, // metadata match only
		{"nonexistent", 0, ""},
		{"code", 1, "code-review"}, // name match
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			results := registry.Search(registry.SearchFilter{Query: tt.query}, entries)
			if len(results) != tt.wantCount {
				t.Errorf("Search(%q) returned %d results, want %d", tt.query, len(results), tt.wantCount)
			}
			if tt.wantFirst != "" && len(results) > 0 && results[0].Name != tt.wantFirst {
				t.Errorf("Search(%q) first result = %q, want %q", tt.query, results[0].Name, tt.wantFirst)
			}
		})
	}
}

func TestSubstringSearch_Scoring(t *testing.T) {
	entries := []registry.SkillIndexEntry{
		{Name: "test", Description: "A test skill for testing."},
		{Name: "other", Description: "Contains test in description.", Metadata: map[string]string{"tag": "test"}},
	}

	results := registry.Search(registry.SearchFilter{Query: "test"}, entries)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// First result should have higher score (name + desc match)
	if results[0].Name != "test" {
		t.Errorf("expected 'test' first (higher score), got %q", results[0].Name)
	}
	if results[0].Score <= results[1].Score {
		t.Errorf("first result score (%f) should be > second (%f)", results[0].Score, results[1].Score)
	}
}

func TestSearch_TagFilter(t *testing.T) {
	entries := []registry.SkillIndexEntry{
		{Name: "deploy-cloud", Description: "deploy", Metadata: map[string]string{"tags": "deploy, acme"}},
		{Name: "deploy-aws", Description: "deploy", Metadata: map[string]string{"tags": "deploy, aws"}},
		{Name: "review-bot", Description: "reviews", Metadata: map[string]string{"tags": "review"}},
	}
	res := registry.Search(registry.SearchFilter{Tags: []string{"deploy"}}, entries)
	if len(res) != 2 {
		t.Fatalf("expected 2 results for tag=deploy, got %d", len(res))
	}
	for _, r := range res {
		if r.Name == "review-bot" {
			t.Errorf("review-bot has no `deploy` tag, should not match: %v", r)
		}
	}

	res = registry.Search(registry.SearchFilter{Tags: []string{"deploy", "acme"}}, entries)
	if len(res) != 1 || res[0].Name != "deploy-cloud" {
		t.Errorf("multi-tag filter should AND tags; got %v", res)
	}
}

func TestSearch_AuthorFilter(t *testing.T) {
	entries := []registry.SkillIndexEntry{
		{Name: "skill-a", Description: "x", Metadata: map[string]string{"author": "acme"}},
		{Name: "skill-b", Description: "x", Metadata: map[string]string{"author": "Acme"}},
		{Name: "skill-c", Description: "x", Metadata: map[string]string{"author": "other"}},
	}
	res := registry.Search(registry.SearchFilter{Author: "acme"}, entries)
	if len(res) != 2 {
		t.Fatalf("expected 2 results (case-insensitive), got %d", len(res))
	}
	for _, r := range res {
		if r.Name == "skill-c" {
			t.Errorf("skill-c has wrong author")
		}
	}
}

func TestSearch_QueryPlusTagAndAuthor(t *testing.T) {
	entries := []registry.SkillIndexEntry{
		{Name: "deploy-cloud", Description: "ship apps fast", Metadata: map[string]string{"author": "acme", "tags": "deploy"}},
		{Name: "deploy-aws", Description: "ship apps fast", Metadata: map[string]string{"author": "aws", "tags": "deploy"}},
	}
	res := registry.Search(registry.SearchFilter{
		Query:  "ship",
		Tags:   []string{"deploy"},
		Author: "acme",
	}, entries)
	if len(res) != 1 || res[0].Name != "deploy-cloud" {
		t.Errorf("expected only deploy-cloud, got %v", res)
	}
}

func TestSearch_EmptyFilterReturnsNothing(t *testing.T) {
	entries := []registry.SkillIndexEntry{{Name: "a", Description: "b"}}
	res := registry.Search(registry.SearchFilter{}, entries)
	if len(res) != 0 {
		t.Errorf("empty filter should return no results, got %d", len(res))
	}
}

func TestSearch_FilterOnlyReturnsAllMatches(t *testing.T) {
	entries := []registry.SkillIndexEntry{
		{Name: "z-skill", Metadata: map[string]string{"author": "acme"}},
		{Name: "a-skill", Metadata: map[string]string{"author": "acme"}},
	}
	res := registry.Search(registry.SearchFilter{Author: "acme"}, entries)
	if len(res) != 2 {
		t.Fatalf("expected both acme skills, got %d", len(res))
	}
	if res[0].Name != "a-skill" {
		t.Errorf("filter-only matches should sort by name; got %s first", res[0].Name)
	}
}

func TestManager_Search(t *testing.T) {
	mock := newMockGitClient()
	mock.trees["HEAD:skills"] = []git.TreeEntry{
		{Name: "code-review", Path: "skills/code-review", IsDir: true},
	}
	mock.blobs["HEAD:skills/code-review/SKILL.md"] = []byte(`---
name: code-review
description: Reviews code in pull requests.
---
`)
	mock.branches = []git.RefInfo{{Name: "main", Hash: "aaa"}}

	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)

	cfg := config.Default()
	cfg.Registries["acme"] = config.RegistryConfig{URL: "https://example.com/acme"}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	mgr := registry.NewManager(mock)
	results, err := mgr.SearchWithFilter(registry.SearchFilter{Query: "review"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Registry != "acme" {
		t.Errorf("expected registry 'acme', got %q", results[0].Registry)
	}
	if results[0].Name != "code-review" {
		t.Errorf("expected 'code-review', got %q", results[0].Name)
	}
}

func TestValidateRegistryName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		// Flat single-segment names — the explicit `--name` lane.
		{"acme", false},
		{"my-registry", false},
		{"reg_1", false},
		{"a", false},
		// v0.5 nested `<org>/<repo>` shape produced by InferRegistryName.
		{"acme-labs/agent-skills", false},
		{"foo/bar", false},
		// Rejections.
		{"", true},
		{"UPPER", true},
		{"Org/Repo", true}, // uppercase in either segment
		{"has space", true},
		{"../evil", true},     // first segment starts with `.`
		{"org/../evil", true}, // second segment starts with `.`
		{"a/b/c", true},       // more than one slash
		{"/repo", true},       // empty leading segment
		{"org/", true},        // empty trailing segment
		{strings.Repeat("a", 129), true},
		{"-starts-hyphen", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := registry.ValidateRegistryName(tt.name)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRegistryName(%q) err=%v, wantErr=%v", tt.name, err, tt.wantErr)
			}
		})
	}
}
