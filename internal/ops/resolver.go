package ops

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/skill"
)

// Attribution is the resolver's answer for an event — which skill
// owns the paths the event touched, plus the relative path inside
// that skill's directory.
type Attribution struct {
	Name     string
	Registry string
	Commit   string
	RelPath  string
}

// Resolver maps event path sets to owning skills. Exposed as an
// interface so tests and per-agent hook hosts can swap the
// implementation.
type Resolver interface {
	// Attribute returns the skill owning at least one of the event's
	// path set. ok is false when no path matches any installed skill
	// AND no session-fallback attribution is available.
	Attribute(e *Event) (Attribution, bool)
}

// target holds a single lockfile entry with its directory canonicalised
// once at construction. This is the hot-path contract: all filesystem
// work (EvalSymlinks) is paid up front, never per event.
type target struct {
	entry *model.LockEntry
	// absDir is filepath.Clean(absolute path); canonicalDir is the
	// filepath.EvalSymlinks of it when the dir exists on disk, or the
	// same as absDir otherwise. We check *both* when matching so an
	// agent's reference to either form attributes correctly.
	absDir       string
	canonicalDir string
	// segCount is the number of path segments in absDir; used to sort
	// targets so the most-specific (deepest) wins when two skills
	// nest. Nested skills aren't a shipping feature, but the cost is
	// trivial and it pins the behaviour.
	segCount int
}

// lockResolver is the production resolver. It reads one or two
// lockfiles at construction, caches each target's canonical dir, and
// remembers the last-attributed skill per session for pathless
// fallback.
type lockResolver struct {
	targets []target

	sessionMu   sync.RWMutex
	sessionLast map[uuid.UUID]Attribution
}

// NewResolver constructs a resolver from the given lockfile paths.
// Multiple paths merge with later-paths shadowing earlier-paths on
// name collision — callers typically pass global first, then local
// (so local overrides).
//
// Non-existent lockfile paths are silently skipped (fresh install).
// Malformed lockfiles return an error because silent data loss would
// be worse than a loud bail.
func NewResolver(lockPaths ...string) (Resolver, error) {
	merged := map[string]*model.LockEntry{}

	for _, p := range lockPaths {
		if p == "" {
			continue
		}
		lf, err := model.ReadLockFile(p)
		if err != nil {
			return nil, err
		}
		for _, e := range lf.Entries() {
			if e == nil || e.Disabled {
				continue
			}
			merged[e.Name] = e
		}
	}

	r := &lockResolver{
		sessionLast: make(map[uuid.UUID]Attribution),
	}
	for _, e := range merged {
		t := buildTarget(e)
		if t.absDir == "" {
			continue
		}
		r.targets = append(r.targets, t)
	}

	// Sort descending by segment count so the most-specific match wins
	// when nested skills exist. For flat layouts the order is stable.
	sort.SliceStable(r.targets, func(i, j int) bool {
		return r.targets[i].segCount > r.targets[j].segCount
	})

	return r, nil
}

// buildTarget canonicalises the effective skill directory for a lock
// entry. Errors from EvalSymlinks (file-not-found) fall back to
// Clean+Abs so disabled-but-present entries still resolve.
func buildTarget(e *model.LockEntry) target {
	raw := skill.EffectiveTarget(e)
	if raw == "" {
		return target{}
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		abs = filepath.Clean(raw)
	}
	abs = filepath.Clean(abs)

	canonical := abs
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		canonical = filepath.Clean(resolved)
	}

	return target{
		entry:        e,
		absDir:       abs,
		canonicalDir: canonical,
		segCount:     strings.Count(abs, string(filepath.Separator)),
	}
}

// Attribute implements Resolver.
func (r *lockResolver) Attribute(e *Event) (Attribution, bool) {
	if r == nil || e == nil {
		return Attribution{}, false
	}

	paths := e.GetPaths()
	// Also consider WorkingDirectory — some events (session_start,
	// notification) have no path payload but a cwd that may fall
	// inside a skill directory.
	if e.WorkingDirectory != "" {
		paths = append(paths, e.WorkingDirectory)
	}

	for _, raw := range paths {
		if raw == "" {
			continue
		}
		if attr, ok := r.attributePath(raw); ok {
			r.rememberSession(e.SessionID, attr)
			return attr, true
		}
	}

	// Session fallback: look up the last attribution for this session.
	if e.SessionID != uuid.Nil {
		r.sessionMu.RLock()
		attr, ok := r.sessionLast[e.SessionID]
		r.sessionMu.RUnlock()
		if ok {
			return attr, true
		}
	}

	return Attribution{}, false
}

// attributePath matches one path against every target. The first match
// wins (targets are sorted most-specific first).
func (r *lockResolver) attributePath(path string) (Attribution, bool) {
	candidate, err := filepath.Abs(path)
	if err != nil {
		candidate = filepath.Clean(path)
	}
	candidate = filepath.Clean(candidate)

	// Canonical form for symlink-deref comparisons. If the file
	// doesn't exist yet (brand-new file the agent is about to write)
	// EvalSymlinks on the *parent* directory is the next best thing.
	canonical := candidate
	if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
		canonical = filepath.Clean(resolved)
	} else if parent := filepath.Dir(candidate); parent != "" && parent != candidate {
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			canonical = filepath.Clean(filepath.Join(resolved, filepath.Base(candidate)))
		}
	}

	for _, t := range r.targets {
		if descends(candidate, t.absDir) || descends(canonical, t.canonicalDir) {
			rel, _ := filepath.Rel(t.absDir, candidate)
			if rel == "" || strings.HasPrefix(rel, "..") {
				// Fall back to the canonical relativisation.
				rel, _ = filepath.Rel(t.canonicalDir, canonical)
			}
			return Attribution{
				Name:     t.entry.Name,
				Registry: t.entry.Registry,
				Commit:   t.entry.Commit,
				RelPath:  rel,
			}, true
		}
	}
	return Attribution{}, false
}

// descends reports whether p is inside (or equal to) root. Both are
// expected to be absolute + cleaned. The check is lexical — we do NOT
// touch the filesystem here; callers that care about symlink-identity
// pass the canonical (EvalSymlinks-resolved) form.
func descends(p, root string) bool {
	if p == "" || root == "" {
		return false
	}
	if p == root {
		return true
	}
	// Use the OS separator so Windows paths match too.
	rootWithSep := root
	if !strings.HasSuffix(rootWithSep, string(filepath.Separator)) {
		rootWithSep += string(filepath.Separator)
	}
	return strings.HasPrefix(p, rootWithSep)
}

// rememberSession updates the session-fallback cache. Bounded at 1024
// entries — sessions are short-lived; this only prevents an unbounded
// leak from long-running _hook processes (which is not the normal
// mode — _hook is one-shot).
func (r *lockResolver) rememberSession(sessionID uuid.UUID, attr Attribution) {
	if sessionID == uuid.Nil {
		return
	}
	r.sessionMu.Lock()
	defer r.sessionMu.Unlock()
	if len(r.sessionLast) >= 1024 {
		// Evict arbitrary entry. Map iteration order is random in Go,
		// so this is effectively random eviction.
		for k := range r.sessionLast {
			delete(r.sessionLast, k)
			break
		}
	}
	r.sessionLast[sessionID] = attr
}

// lockfileExists returns true if path exists and is a regular file.
// Package-internal helper used by the funnel wiring to decide whether
// to bother passing a given path to NewResolver.
func lockfileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// ensureFields is a tiny helper: after Attribute succeeds, copy the
// attribution into the event. Separated so tests can assert the
// resolver's output directly without touching event mutation.
//
// All four attribution fields are overwritten unconditionally (including
// with empty strings) so re-attribution — e.g. via the session-fallback
// path after an event was tentatively tagged with a different skill —
// never leaves stale metadata behind.
func ensureFields(e *Event, attr Attribution) {
	if e == nil {
		return
	}
	e.SkillName = attr.Name
	e.SkillRegistry = attr.Registry
	e.SkillCommit = attr.Commit
	e.SkillPath = attr.RelPath
}
