package registry

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/pkg/skillspec"
)

var ErrIndexBuildFailed = errors.New("index build failed")

// SkillVersionInfo holds branch and tag lists for a skill.
type SkillVersionInfo struct {
	Branches []string `json:"branches"`
	Tags     []string `json:"tags"`
}

// SkillIndexEntry represents a single skill discovered in a registry.
type SkillIndexEntry struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Path        string            `json:"path"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Versions    SkillVersionInfo  `json:"versions"`
}

// SkippedSkill is re-exported from internal/model so registry consumers don't
// need a second import. The cache and indexer use this name freely; the model
// package owns the canonical type so RegistryStatus can carry it without
// creating an import cycle.
type SkippedSkill = model.SkippedSkill

// SearchResult represents a search hit with relevance scoring.
type SearchResult struct {
	SkillIndexEntry
	Registry string  `json:"registry"`
	Score    float64 `json:"score"`
}

// Indexer builds skill indexes from bare git repositories.
type Indexer struct {
	Git git.GitClient
}

// NewIndexer creates a new Indexer.
func NewIndexer(gitClient git.GitClient) *Indexer {
	return &Indexer{Git: gitClient}
}

// BuildIndex reads the bare repo at HEAD, discovers skills, and returns index
// entries plus a list of candidate directories that could not be indexed (no
// SKILL.md, parse error, etc). The skipped list is informational — BuildIndex
// only returns an error for repo-level failures, not per-skill ones.
func (idx *Indexer) BuildIndex(repoPath string) ([]SkillIndexEntry, []SkippedSkill, error) {
	defaultBranch, err := idx.Git.DefaultBranch(repoPath)
	if err != nil {
		defaultBranch = "main"
	}

	// Try skills/ subdirectory first
	entries, err := idx.Git.ListTree(repoPath, "HEAD", "skills")
	if err != nil {
		// No skills/ directory — try root-level SKILL.md
		return idx.buildFromRoot(repoPath, defaultBranch)
	}

	var skills []SkillIndexEntry
	var skipped []SkippedSkill
	for _, entry := range entries {
		if !entry.IsDir {
			continue
		}
		skillMDPath := fmt.Sprintf("skills/%s/SKILL.md", entry.Name)
		blob, err := idx.Git.ReadBlob(repoPath, "HEAD", skillMDPath)
		if err != nil {
			skipped = append(skipped, SkippedSkill{
				Name:   entry.Name,
				Path:   fmt.Sprintf("skills/%s", entry.Name),
				Reason: "missing SKILL.md",
			})
			continue
		}

		parsed, err := skillspec.Parse(string(blob))
		if err != nil {
			skipped = append(skipped, SkippedSkill{
				Name:   entry.Name,
				Path:   skillMDPath,
				Reason: err.Error(),
			})
			continue
		}

		skills = append(skills, SkillIndexEntry{
			Name:        parsed.Frontmatter.Name,
			Description: parsed.Frontmatter.Description,
			Path:        fmt.Sprintf("skills/%s", entry.Name),
			Metadata:    parsed.Frontmatter.Metadata,
		})
	}

	// Populate version info
	idx.populateVersions(repoPath, skills)

	return skills, skipped, nil
}

func (idx *Indexer) buildFromRoot(repoPath, defaultBranch string) ([]SkillIndexEntry, []SkippedSkill, error) {
	blob, err := idx.Git.ReadBlob(repoPath, "HEAD", "SKILL.md")
	if err != nil {
		return []SkillIndexEntry{}, nil, nil
	}

	parsed, err := skillspec.Parse(string(blob))
	if err != nil {
		return nil, []SkippedSkill{{
			Name:   ".",
			Path:   "SKILL.md",
			Reason: err.Error(),
		}}, fmt.Errorf("%w: parse root SKILL.md: %v", ErrIndexBuildFailed, err)
	}

	skills := []SkillIndexEntry{{
		Name:        parsed.Frontmatter.Name,
		Description: parsed.Frontmatter.Description,
		Path:        ".",
		Metadata:    parsed.Frontmatter.Metadata,
	}}

	idx.populateVersions(repoPath, skills)

	return skills, nil, nil
}

func (idx *Indexer) populateVersions(repoPath string, skills []SkillIndexEntry) {
	branches, _ := idx.Git.ListBranches(repoPath)
	tags, _ := idx.Git.ListTags(repoPath)

	branchNames := make([]string, len(branches))
	for i, b := range branches {
		branchNames[i] = b.Name
	}
	tagNames := make([]string, len(tags))
	for i, t := range tags {
		tagNames[i] = t.Name
	}

	for i := range skills {
		skills[i].Versions = SkillVersionInfo{
			Branches: branchNames,
			Tags:     tagNames,
		}
	}
}

// SearchFilter bundles the user's search intent. At least one of Query,
// Tags, or Author must be set — Search returns nil if the filter is empty
// to avoid accidental full-registry dumps.
type SearchFilter struct {
	Query  string
	Tags   []string // matched against metadata["tags"] (comma-separated)
	Author string   // matched against metadata["author"]
}

// Search scores entries by substring match on name/description/metadata and
// filters by tag and author when set. Tags and Author are hard filters —
// a non-matching entry is dropped even if it has a perfect query score.
//
// Scoring (query hits only):
//
//	name     +3.0
//	desc     +1.0
//	metadata +0.5 (once total)
//
// Filter-only searches (no query) assign a flat score so all matches are
// returned in stable alphabetical order.
func Search(filter SearchFilter, entries []SkillIndexEntry) []SearchResult {
	hasQuery := strings.TrimSpace(filter.Query) != ""
	hasTagFilter := len(filter.Tags) > 0
	author := strings.ToLower(strings.TrimSpace(filter.Author))
	hasAuthor := author != ""
	if !hasQuery && !hasTagFilter && !hasAuthor {
		return nil
	}

	need := make(map[string]struct{}, len(filter.Tags))
	for _, tag := range filter.Tags {
		t := strings.ToLower(strings.TrimSpace(tag))
		if t != "" {
			need[t] = struct{}{}
		}
	}
	q := strings.ToLower(strings.TrimSpace(filter.Query))

	var results []SearchResult
	for _, entry := range entries {
		if hasTagFilter {
			if !entryHasAllTags(entry, need) {
				continue
			}
		}
		if hasAuthor && strings.ToLower(strings.TrimSpace(entry.Metadata["author"])) != author {
			continue
		}

		var score float64
		if hasQuery {
			if strings.Contains(strings.ToLower(entry.Name), q) {
				score += 3.0
			}
			if strings.Contains(strings.ToLower(entry.Description), q) {
				score += 1.0
			}
			for _, v := range entry.Metadata {
				if strings.Contains(strings.ToLower(v), q) {
					score += 0.5
					break
				}
			}
			if score == 0 {
				continue
			}
		} else {
			score = 1.0
		}

		results = append(results, SearchResult{SkillIndexEntry: entry, Score: score})
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].Name < results[j].Name
	})
	return results
}

// SubstringSearch is preserved for callers that only want query-based search.
// New code should use Search with a full SearchFilter.
func SubstringSearch(query string, entries []SkillIndexEntry) []SearchResult {
	return Search(SearchFilter{Query: query}, entries)
}

// entryHasAllTags is true when every required tag appears in the entry's
// `metadata.tags` field (comma-separated, case-insensitive).
func entryHasAllTags(entry SkillIndexEntry, required map[string]struct{}) bool {
	have := make(map[string]struct{})
	for _, tag := range parseTags(entry.Metadata["tags"]) {
		have[tag] = struct{}{}
	}
	for t := range required {
		if _, ok := have[t]; !ok {
			return false
		}
	}
	return true
}

// parseTags splits "deploy, demo , vercel" into ["deploy", "demo", "vercel"]
// with trimming and lowercasing. Used by Search and directly testable.
func parseTags(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		t := strings.ToLower(strings.TrimSpace(part))
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}
