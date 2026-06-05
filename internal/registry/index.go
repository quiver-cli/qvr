package registry

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/quiver-cli/qvr/internal/git"
	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/pkg/skillspec"
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
	// RootCoexists is true only for a root-layout skill (Path ".") that shares
	// the repo with sibling skill directories. It tells the scan gate and
	// installer to scope this skill to its own content rather than the whole
	// repo (which would also pull in the siblings and unrelated app code).
	RootCoexists bool `json:"rootCoexists,omitempty"`
}

// SkillScopePaths returns the repo-relative paths that make up a skill's
// installable and scannable content. It delegates to model.SkillScopePaths, the
// single source of truth also used by the installer (off the lock entry).
func SkillScopePaths(e SkillIndexEntry) []string {
	return model.SkillScopePaths(e.Path, e.RootCoexists)
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
	blobs, err := idx.Git.ListBlobsRecursive(repoPath, "HEAD", "")
	if err != nil {
		// Unreadable or empty repo — nothing to index. Per-repo discovery
		// failures are not fatal: callers treat an empty index as "no skills".
		return []SkillIndexEntry{}, nil, nil
	}

	// 1. Every SKILL.md anywhere in the tree maps to its containing directory
	//    ("." for a root SKILL.md). This is the "search all SKILL.md files" pass.
	var skillDirs []string
	for _, b := range blobs {
		if path.Base(b.Path) != "SKILL.md" {
			continue
		}
		skillDirs = append(skillDirs, path.Dir(b.Path))
	}
	// Sort so any ancestor directory precedes its descendants — required for the
	// single-pass prune below to be correct and deterministic.
	sort.Strings(skillDirs)

	// 2. Prune nested skills. A SKILL.md inside another skill's subtree is that
	//    skill's own asset (references/, scripts/, examples, …), not a separate
	//    skill. The repo ROOT is the sole exception: it may itself be a skill yet
	//    still parents sibling skills, so it never prunes its children.
	var kept []string
	for _, d := range skillDirs {
		if d == "." {
			kept = append(kept, d)
			continue
		}
		nested := false
		for _, k := range kept {
			if k == "." {
				continue
			}
			if d == k || strings.HasPrefix(d, k+"/") {
				nested = true
				break
			}
		}
		if !nested {
			kept = append(kept, d)
		}
	}

	// 3. Parse, validate, and dedup the kept skill directories.
	var skills []SkillIndexEntry
	var skipped []SkippedSkill
	seen := make(map[string]string) // skill name -> path of first occurrence
	for _, d := range kept {
		mdPath := "SKILL.md"
		if d != "." {
			mdPath = d + "/SKILL.md"
		}
		blob, err := idx.Git.ReadBlob(repoPath, "HEAD", mdPath)
		if err != nil {
			skipped = append(skipped, SkippedSkill{Name: path.Base(d), Path: mdPath, Reason: "missing SKILL.md"})
			continue
		}

		parsed, err := skillspec.Parse(string(blob))
		if err != nil {
			skipped = append(skipped, SkippedSkill{Name: path.Base(d), Path: mdPath, Reason: err.Error()})
			continue
		}

		// A non-root skill must live in a directory matching its frontmatter
		// name, otherwise the install-time validator (validateNameDirMatch)
		// would reject it. Surface the mismatch instead of indexing a skill that
		// can never install. The root is exempt — its directory basename is the
		// staging/worktree dir, and the installer overrides the name from the
		// canonical index entry before validation.
		if d != "." && parsed.Frontmatter.Name != path.Base(d) {
			skipped = append(skipped, SkippedSkill{
				Name:   parsed.Frontmatter.Name,
				Path:   mdPath,
				Reason: fmt.Sprintf("frontmatter name %q does not match directory %q", parsed.Frontmatter.Name, path.Base(d)),
			})
			continue
		}

		if prior, ok := seen[parsed.Frontmatter.Name]; ok {
			skipped = append(skipped, SkippedSkill{
				Name:   parsed.Frontmatter.Name,
				Path:   mdPath,
				Reason: fmt.Sprintf("duplicate skill name (already indexed at %s)", prior),
			})
			continue
		}
		seen[parsed.Frontmatter.Name] = d

		skills = append(skills, SkillIndexEntry{
			Name:        parsed.Frontmatter.Name,
			Description: parsed.Frontmatter.Description,
			Path:        d,
			Metadata:    parsed.Frontmatter.Metadata,
		})
	}

	// 4. Flag a root skill that coexists with siblings so scan/install can scope
	//    it to its own content rather than the entire repository.
	if len(skills) > 1 {
		for i := range skills {
			if skills[i].Path == "." {
				skills[i].RootCoexists = true
			}
		}
	}

	idx.populateVersions(repoPath, skills)

	return skills, skipped, nil
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
			Tags:     tagsForSkill(skills[i].Name, tagNames),
		}
	}
}

// tagsForSkill returns the tags that belong to a given skill in a (possibly
// multi-skill) registry: its per-skill-namespaced tags "<name>/<v>" plus any
// bare (un-namespaced) tags — the latter are what legacy single-skill repos
// produced and remain shared. A tag namespaced for ANOTHER skill ("beta/v1")
// is excluded so two skills no longer claim each other's versions (issue #152).
// Full ref names are preserved so resolution can check them out directly.
func tagsForSkill(name string, all []string) []string {
	out := make([]string, 0, len(all))
	for _, t := range all {
		if model.TagBelongsToSkill(t, name) {
			out = append(out, t)
		}
	}
	return out
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

// parseTags splits "deploy, demo , acme" into ["deploy", "demo", "acme"]
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
