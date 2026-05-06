package privacy

import (
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// PathChecker flags events whose path set intersects any configured
// sensitive glob. When it fires, the Decision sets IsSensitive=true
// and StripContent=true — path-based sensitivity is the cue to drop
// content entirely, since anything touching `.env` or `.ssh/id_rsa`
// is presumed unsafe to persist.
//
// Matching is case-insensitive on the path segments themselves (we
// lower-case before matching) to catch `.ENV` or `.SSH/id_rsa` on
// case-insensitive filesystems, while pattern strings stay lowercase
// in Default*.
type PathChecker struct {
	patterns []string
}

// NewPathChecker returns a checker over the given patterns. Patterns
// use doublestar syntax; nil or empty is valid (checker becomes a no-op).
func NewPathChecker(patterns []string) *PathChecker {
	return &PathChecker{patterns: patterns}
}

// Evaluate returns an IsSensitive/StripContent decision on first match.
// It does not continue scanning after the first hit — a single sensitive
// path is enough to decide the event's fate.
func (c *PathChecker) Evaluate(e Event) Decision {
	if c == nil || len(c.patterns) == 0 {
		return Decision{}
	}
	for _, raw := range e.GetPaths() {
		if raw == "" {
			continue
		}
		p := normalizePath(raw)
		for _, pat := range c.patterns {
			match, err := doublestar.PathMatch(pat, p)
			if err != nil {
				// malformed pattern — skip; caller should surface
				// compile errors at construction time, not here
				continue
			}
			if match {
				return Decision{
					IsSensitive:  true,
					StripContent: true,
					MatchedRules: []string{pat},
				}
			}
		}
	}
	return Decision{}
}

// normalizePath lower-cases and forward-slashes the path so doublestar
// globs ("**/.env") match regardless of OS or casing.
func normalizePath(p string) string {
	p = filepath.ToSlash(strings.TrimSpace(p))
	p = strings.ToLower(p)
	// Strip a trailing slash so "secrets/" matches "**/secrets/**" via
	// the directory form.
	p = strings.TrimRight(p, "/")
	return p
}
