package registry_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/registry"
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

func (m *mockGitClient) BareClone(ctx context.Context, url, path string) error { return nil }
func (m *mockGitClient) Clone(ctx context.Context, url, path string) error     { return nil }
func (m *mockGitClient) SubdirClone(ctx context.Context, url, ref, subpath, dest string) error {
	return nil
}
func (m *mockGitClient) Fetch(ctx context.Context, repoPath string) error { return nil }
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

func (m *mockGitClient) HeadCommit(repoPath string) (string, error) {
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

func TestBuildIndex_NoSkillMDSkipped(t *testing.T) {
	mock := newMockGitClient()
	mock.trees["HEAD:skills"] = []git.TreeEntry{
		{Name: "has-skill", Path: "skills/has-skill", IsDir: true},
		{Name: "no-skill", Path: "skills/no-skill", IsDir: true},
	}
	mock.blobs["HEAD:skills/has-skill/SKILL.md"] = []byte(`---
name: has-skill
description: Has a SKILL.md.
---
`)
	// no-skill has no SKILL.md blob

	indexer := registry.NewIndexer(mock)
	skills, skipped, err := indexer.BuildIndex("/fake/path")
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}

	if len(skills) != 1 {
		t.Errorf("expected 1 skill, got %d", len(skills))
	}
	if len(skipped) != 1 || skipped[0].Name != "no-skill" {
		t.Errorf("expected no-skill in skipped list, got %+v", skipped)
	}
	if skipped[0].Reason != "missing SKILL.md" {
		t.Errorf("reason = %q, want %q", skipped[0].Reason, "missing SKILL.md")
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
			results := registry.SubstringSearch(tt.query, entries)
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

	results := registry.SubstringSearch("test", entries)

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
		{Name: "deploy-vercel", Description: "deploy", Metadata: map[string]string{"tags": "deploy, vercel"}},
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

	res = registry.Search(registry.SearchFilter{Tags: []string{"deploy", "vercel"}}, entries)
	if len(res) != 1 || res[0].Name != "deploy-vercel" {
		t.Errorf("multi-tag filter should AND tags; got %v", res)
	}
}

func TestSearch_AuthorFilter(t *testing.T) {
	entries := []registry.SkillIndexEntry{
		{Name: "skill-a", Description: "x", Metadata: map[string]string{"author": "vercel"}},
		{Name: "skill-b", Description: "x", Metadata: map[string]string{"author": "Vercel"}},
		{Name: "skill-c", Description: "x", Metadata: map[string]string{"author": "other"}},
	}
	res := registry.Search(registry.SearchFilter{Author: "vercel"}, entries)
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
		{Name: "deploy-vercel", Description: "ship apps fast", Metadata: map[string]string{"author": "vercel", "tags": "deploy"}},
		{Name: "deploy-aws", Description: "ship apps fast", Metadata: map[string]string{"author": "aws", "tags": "deploy"}},
	}
	res := registry.Search(registry.SearchFilter{
		Query:  "ship",
		Tags:   []string{"deploy"},
		Author: "vercel",
	}, entries)
	if len(res) != 1 || res[0].Name != "deploy-vercel" {
		t.Errorf("expected only deploy-vercel, got %v", res)
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
		{Name: "z-skill", Metadata: map[string]string{"author": "vercel"}},
		{Name: "a-skill", Metadata: map[string]string{"author": "vercel"}},
	}
	res := registry.Search(registry.SearchFilter{Author: "vercel"}, entries)
	if len(res) != 2 {
		t.Fatalf("expected both vercel skills, got %d", len(res))
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
	results, err := mgr.Search("review")
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
		{"vercel-labs/agent-skills", false},
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
