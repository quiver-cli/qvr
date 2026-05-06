package registry

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/raks097/quiver/internal/config"
)

var registryNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// slugRe matches slugs produced by URLToSlug: lowercase alphanumerics with
// dots, dashes, and underscores. The "--" produced from "/" and ":" is
// covered by the dash class.
var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// ValidateRegistryName checks that a registry name is safe for use as a directory name.
func ValidateRegistryName(name string) error {
	if name == "" {
		return errors.New("name cannot be empty")
	}
	if len(name) > 64 {
		return fmt.Errorf("name %q exceeds 64 characters", name)
	}
	if !registryNameRe.MatchString(name) {
		return fmt.Errorf("name %q must be lowercase alphanumeric, hyphens, or underscores", name)
	}
	return nil
}

// ValidateSlug rejects empty strings, traversal segments, and characters
// outside the URLToSlug output alphabet so a hostile URL can't be turned into
// a path that escapes its parent directory.
func ValidateSlug(slug string) error {
	if slug == "" {
		return errors.New("slug cannot be empty")
	}
	if len(slug) > 256 {
		return fmt.Errorf("slug exceeds 256 characters")
	}
	if strings.Contains(slug, "..") || strings.ContainsAny(slug, `/\`) {
		return fmt.Errorf("slug %q contains path separators or traversal", slug)
	}
	if !slugRe.MatchString(slug) {
		return fmt.Errorf("slug %q has disallowed characters", slug)
	}
	return nil
}

// RegistryPath returns the bare clone path for a named registry.
func RegistryPath(name string) string {
	return filepath.Join(config.Dir(), "registries", name+".git")
}

// URLToSlug converts a URL to a filesystem-safe slug.
func URLToSlug(url string) string {
	s := strings.TrimPrefix(url, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "git@")
	s = strings.TrimSuffix(s, ".git")
	s = strings.ReplaceAll(s, "/", "--")
	s = strings.ReplaceAll(s, ":", "--")
	return s
}

// WorktreesRoot returns the base directory holding all worktrees.
func WorktreesRoot() string {
	return filepath.Join(config.Dir(), "worktrees")
}

// SubdirRoot returns the base directory holding bare clones used to back
// `qvr add <subdir-url>` installs. Kept separate from `registries/` so a
// later `qvr registry add` of the same URL doesn't collide with — or
// silently inherit from — an ad-hoc subdir install.
func SubdirRoot() string {
	return filepath.Join(config.Dir(), "subdir")
}

// SubdirRepoPath returns the bare clone path for a slug under SubdirRoot().
// Returns an error if slug fails ValidateSlug — guards against a hostile URL
// being shaped into a directory that escapes SubdirRoot.
func SubdirRepoPath(slug string) (string, error) {
	if err := ValidateSlug(slug); err != nil {
		return "", err
	}
	return filepath.Join(SubdirRoot(), slug+".git"), nil
}

// WorktreePath returns the expected worktree path for a skill pinned at ref.
// Ref and skill are slug-ified with the same "--" replacement used elsewhere
// so refs containing slashes (e.g. "feature/x") can't collide with a literal
// "feature-x" ref.
func WorktreePath(registry, skill, ref string) string {
	safeRef := slugSegment(ref)
	safeSkill := slugSegment(skill)
	name := fmt.Sprintf("%s--%s--%s", registry, safeSkill, safeRef)
	return filepath.Join(WorktreesRoot(), name)
}

func slugSegment(s string) string {
	s = strings.ReplaceAll(s, "/", "--")
	s = strings.ReplaceAll(s, ":", "--")
	return s
}
