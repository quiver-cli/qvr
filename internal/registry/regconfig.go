package registry

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/astra-sh/qvr/internal/git"
)

// RegistryFile is the parsed shape of a repo's top-level registry.yaml — the
// optional manifest the registry docs describe. When present, skill discovery
// is scoped to SkillsDir (plus Ignore filtering) instead of walking the whole
// tree, so fixtures, vendored code, and test data never reach the consumer
// surface (#244). Parsing is deliberately loose: documented metadata keys
// (description, maintainers, settings…) are accepted and ignored.
type RegistryFile struct {
	Name string `yaml:"name"`
	// SkillsDir is the repo-relative directory skills live under. Defaults to
	// "skills". The repo root itself ("." ) is always allowed too, so a
	// single-skill root-layout repo can still carry a registry.yaml.
	SkillsDir string `yaml:"skills-dir"`
	// Ignore lists path.Match globs evaluated against each candidate skill
	// directory (repo-relative, slash-separated); matches are skipped.
	Ignore   []string       `yaml:"ignore"`
	Settings map[string]any `yaml:"settings"`
}

// registryFileName is the manifest path probed at the repo root.
const registryFileName = "registry.yaml"

// loadRegistryFile reads and parses registry.yaml from the bare repo's HEAD.
// Returns (nil, nil) when the file doesn't exist — whole-tree discovery
// applies. A present-but-unparsable manifest returns an error so the caller
// can surface it instead of silently mis-scoping.
func loadRegistryFile(gc git.GitClient, repoPath string) (*RegistryFile, error) {
	data, err := gc.ReadBlob(repoPath, "HEAD", registryFileName)
	if err != nil {
		if errors.Is(err, git.ErrBlobNotFound) {
			return nil, nil
		}
		// Unreadable repo state degrades to whole-tree discovery, same as a
		// missing manifest — BuildIndex already tolerates unreadable repos.
		return nil, nil
	}
	rf := &RegistryFile{}
	if err := yaml.Unmarshal(data, rf); err != nil {
		return nil, fmt.Errorf("parse %s: %w", registryFileName, err)
	}
	if rf.SkillsDir == "" {
		rf.SkillsDir = "skills"
	}
	rf.SkillsDir = strings.Trim(path.Clean(rf.SkillsDir), "/")
	return rf, nil
}

// applyRegistryScope filters candidate skill dirs to the manifest's SkillsDir
// (the repo root "." stays eligible) and drops Ignore-glob matches. Excluded
// dirs come back as informational skips so `registry add` never reports a
// silent 0-skill mystery.
func applyRegistryScope(rf *RegistryFile, skillDirs []string) (kept []string, skipped []SkippedSkill) {
	for _, d := range skillDirs {
		switch {
		case ignoreGlobMatch(rf.Ignore, d):
			skipped = append(skipped, SkippedSkill{
				Name:   path.Base(d),
				Path:   d,
				Reason: fmt.Sprintf("ignored by %s ignore pattern", registryFileName),
			})
		case rf.SkillsDir == "." || d == "." || d == rf.SkillsDir || strings.HasPrefix(d, rf.SkillsDir+"/"):
			kept = append(kept, d)
		default:
			skipped = append(skipped, SkippedSkill{
				Name:   path.Base(d),
				Path:   d,
				Reason: fmt.Sprintf("outside skills-dir %q (%s)", rf.SkillsDir, registryFileName),
			})
		}
	}
	return kept, skipped
}

// ignoreGlobMatch reports whether dir matches any of the manifest's ignore
// globs, evaluated with path.Match against the repo-relative directory.
func ignoreGlobMatch(globs []string, dir string) bool {
	for _, g := range globs {
		if ok, err := path.Match(g, dir); err == nil && ok {
			return true
		}
	}
	return false
}

// fixturePathSegments are directory names that mark test fixtures rather than
// consumable skills. Skill dirs under any of these segments are always
// excluded from indexing — a repo's scanner fixtures (deliberately malicious
// SKILL.md files) must never show up in search/install (#244).
var fixturePathSegments = map[string]bool{
	"testdata": true,
	"fixtures": true,
}

// excludeFixturePaths drops skill dirs that live under a testdata/ or
// fixtures/ path segment at any depth, surfacing each as an informational
// skip.
func excludeFixturePaths(skillDirs []string) (kept []string, skipped []SkippedSkill) {
	for _, d := range skillDirs {
		if seg := fixtureSegmentIn(d); seg != "" {
			skipped = append(skipped, SkippedSkill{
				Name:   path.Base(d),
				Path:   d,
				Reason: fmt.Sprintf("under %s/ — test fixtures are excluded from indexing", seg),
			})
			continue
		}
		kept = append(kept, d)
	}
	return kept, skipped
}

// fixtureSegmentIn returns the first fixture-marking path segment in dir, or
// "" when dir is a regular skill location.
func fixtureSegmentIn(dir string) string {
	for seg := range strings.SplitSeq(dir, "/") {
		if fixturePathSegments[seg] {
			return seg
		}
	}
	return ""
}
