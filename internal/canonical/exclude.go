package canonical

import "strings"

// ExcludedFromSubtree lists paths that are NOT part of a skill's canonical
// subtree digest. These are wrapper artifacts produced *after* the digest
// is computed (the signature can't be inside the thing it signs) or that
// live outside the subtree (the lockfile). Computing the subtree hash with
// these included would create a circular dependency when signing.
//
// Paths are relative to the skill subtree root, not the worktree root.
var ExcludedFromSubtree = map[string]bool{
	"qvr.sig":                  true,
	".quiver-attestation.json": true,
}

// IsExcluded reports whether a path (relative to the subtree root) should
// be skipped during canonical-hash computation.
//
// Also excludes anything under `.git/` — these are git internal bookkeeping
// files that change without any user action (index updates, gc, ref updates)
// and have nothing to do with the skill content. Without this exclusion,
// `qvr edit`'s eject hash diverges from `qvr lock verify`'s recomputation
// the instant the .git/index ticks (issue #80).
func IsExcluded(relPath string) bool {
	if ExcludedFromSubtree[relPath] {
		return true
	}
	if relPath == ".git" || strings.HasPrefix(relPath, ".git/") {
		return true
	}
	return false
}
