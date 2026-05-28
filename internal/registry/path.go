package registry

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/raks097/quiver/internal/config"
)

// registryNameSegmentRe is the per-segment shape: lowercase alphanumeric
// optionally followed by `[a-z0-9_-]*` and ending alphanumeric (single-char
// segments are allowed by the leading-class match).
var registryNameSegmentRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// slugRe matches slugs produced by URLToSlug: lowercase alphanumerics with
// dots, dashes, and underscores. The "--" produced from "/" and ":" is
// covered by the dash class.
var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// ValidateRegistryName checks that a registry name is safe for use as a
// (possibly nested) directory name. v0.5 auto-naming produces `<org>/<repo>`
// so registries land under `~/.quiver/registries/<org>/<repo>.git/`. Each
// segment must match registryNameSegmentRe, and only one `/` is permitted
// — flat single-segment names (the explicit `--name` lane) still work.
func ValidateRegistryName(name string) error {
	if name == "" {
		return errors.New("name cannot be empty")
	}
	if len(name) > 128 {
		return fmt.Errorf("name %q exceeds 128 characters", name)
	}
	segments := strings.Split(name, "/")
	if len(segments) > 2 {
		return fmt.Errorf("name %q has more than one `/` — at most `<org>/<repo>` is allowed", name)
	}
	for _, seg := range segments {
		if !registryNameSegmentRe.MatchString(seg) {
			return fmt.Errorf("name %q: segment %q must be lowercase alphanumeric, hyphens, or underscores", name, seg)
		}
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

// RegistryPath returns the bare clone path for a named registry. Under the
// v4 layout this is the single home for all bare clones — single-skill and
// multi-skill repos alike. The legacy SubdirRoot/standalone roots are gone.
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

// InferRegistryName produces the auto-name for `qvr registry add <url>` —
// `<org>/<repo>`, lowercased, with characters outside the registry-name
// alphabet replaced by `-`. The last two non-empty path segments of the
// clone URL become the org and repo respectively; this handles
// `https://host/org/repo[.git]`, `git@host:org/repo.git`, and
// `ssh://git@host/org/repo.git` uniformly. Returns "" when the URL has
// no usable org/repo shape so callers can fall back to requiring an
// explicit `--name` flag.
//
// On disk this nests as `~/.quiver/registries/<org>/<repo>.git/` so all
// of one org's registries live under a single parent — clean to browse
// and cross-platform (the only path separator involved is `/`, which
// filepath.Join translates to the OS-native form).
func InferRegistryName(rawURL string) string {
	s := strings.TrimSpace(rawURL)
	if s == "" {
		return ""
	}
	s = strings.TrimSuffix(s, ".git")

	// Strip scheme + host. For URL-shaped inputs (`scheme://...`), the
	// authority section ends at the first `/`. For scp-style SSH
	// (`git@host:org/repo`), the authority ends at the first `:`.
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
		slash := strings.Index(s, "/")
		if slash < 0 {
			return ""
		}
		s = s[slash+1:]
	} else if at := strings.LastIndex(s, "@"); at >= 0 {
		s = s[at+1:]
		if colon := strings.Index(s, ":"); colon >= 0 {
			s = s[colon+1:]
		}
	}

	parts := strings.Split(strings.Trim(s, "/"), "/")
	var nonEmpty []string
	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	if len(nonEmpty) < 2 {
		return ""
	}
	org := sanitizeRegistryNameSegment(nonEmpty[len(nonEmpty)-2])
	repo := sanitizeRegistryNameSegment(nonEmpty[len(nonEmpty)-1])
	if org == "" || repo == "" {
		return ""
	}
	return org + "/" + repo
}

// sanitizeRegistryNameSegment lowercases the input and replaces any rune
// outside `[a-z0-9_-]` with `-`. Leading non-alphanumerics are trimmed so
// the final slug starts with a class that satisfies ValidateRegistryName.
func sanitizeRegistryNameSegment(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := b.String()
	for len(out) > 0 {
		c := out[0]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			break
		}
		out = out[1:]
	}
	return out
}

// WorktreesRoot returns the base directory holding all worktrees.
func WorktreesRoot() string {
	return filepath.Join(config.Dir(), "worktrees")
}

// WorktreePath returns the worktree path for a skill pinned at sha. The
// caller passes the resolved short SHA (7 chars by convention) — the path
// uses it directly as the cache key, so two projects pinning the same commit
// share one worktree and different SHAs (even on the same branch) never
// collide. Ref names live only in the lockfile as human labels.
//
// v0.5 layout: `~/.quiver/worktrees/<registry>/<skill>/<sha7>/`. With the
// `<org>/<repo>` registry shape, the full path nests four levels under
// WorktreesRoot — `<org>/<repo>/<skill>/<sha7>` — matching the registries
// directory shape so `~/.quiver/` is easy to browse and to wipe by org.
//
// The skill and sha components go through slugSegment as a defence-in-depth
// against hostile refs slipping in through transitional code paths; the
// registry component is trusted because ValidateRegistryName has already
// rejected anything that could escape the worktrees subtree.
func WorktreePath(registry, skill, sha string) string {
	safeSkill := slugSegment(skill)
	safeSha := slugSegment(sha)
	return filepath.Join(WorktreesRoot(), registry, safeSkill, safeSha)
}

// ShortSHA returns the cache-key form of a commit SHA — 7 hex characters,
// matching git's default abbreviation. Returns the input unchanged when it's
// already shorter than 7 chars (defensive: an empty or stub SHA should pass
// through so the resulting worktree path remains diagnosable).
func ShortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func slugSegment(s string) string {
	s = strings.ReplaceAll(s, "/", "--")
	s = strings.ReplaceAll(s, ":", "--")
	return s
}
