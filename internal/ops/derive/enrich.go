package derive

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/ops"
	reg "github.com/astra-sh/qvr/internal/registry"
)

// EnrichSkillIdentity promotes full skill identity onto SKILL/TOOL spans whose
// recorded load path PROVES which artifact the agent ran, by resolving the
// span's skill.name against the calling project's qvr.lock (falling back to
// the user-global lock) and checking the load path for containment in that
// entry's managed worktree. The lockfile pins each skill to registry + commit
// + ref + subtreeHash, so two registries that both ship "code-review", or two
// pinned versions of the same skill across projects, become distinguishable
// in logs/spans/UI/OTLP without scraping raw command strings (issue #146).
//
// It is intentionally a SEPARATE pass from DeriveSession, not folded into the
// derivers: a Deriver must be PURE (same rows in → same spans out), and the
// lockfile is external state not carried in the rows. Enrichment is applied at
// the persistence/display boundaries (capture + `qvr audit spans`), where
// reflecting the current pin is what callers want.
//
// Identity is proof-gated: the presence of skill.version IS the verification
// signal (version present ⇔ proven; no separate boolean). Attributes added
// when — and only when — the span's skill.load_path resolves into the lock
// entry's worktree:
//
//	skill.registry     — the registry the skill came from (e.g. "raks")
//	skill.version      — the requested ref (branch/tag), or the short pinned
//	                     SHA when the entry has no ref — always set on proof,
//	                     so "has skill.version" is the one verification test
//	skill.commit       — the pinned git SHA at the entry's HEAD
//	skill.source       — the fetch coordinate (git URL or local path)
//	skill.subtree_hash — canonical content hash of the installed subtree
//	skill.canonical    — upstream skill name when installed under an alias
//
// Everything else keeps skill.name (and skill.load_path, when recorded) as
// evidence and gains NO identity fields: a span with no load path (an agent
// that never records what it read), a path that resolves outside the entry's
// worktree (a global eject shadowing the project install, a copy from another
// registry, a drifted sha), or a name absent from every lock (built-in agent
// skills, skills installed by another tool). qvr never attests a
// registry+commit+subtree_hash the agent didn't provably run (#149). The
// unproven name→lock pin is still useful context, but it is a DISPLAY-TIME
// join (UI/CLI resolve name→lock at render and label it as the current pin),
// never persisted onto spans as identity.
// snap optionally carries the session's ingest-time identity snapshot
// (skill name → frozen entry). Symlink-origin evidence (an agent-dir path
// like claude's base-directory line) can only be resolved against the
// filesystem AS IT IS NOW, so a rederive after a version move would rewrite
// history; the snapshot — harvested at first ingest, when resolution still
// matched run time — wins for that evidence class. Transcript-pinned store
// paths (the sha is in the recorded path itself) stay path-truth and ignore
// the snapshot. Pass nil when no snapshot exists (first ingest, display-time
// re-derivation of unpersisted rows).
func EnrichSkillIdentity(spans []Span, rows []*ops.RawTrace, snap map[string]*model.LockEntry) {
	if len(spans) == 0 {
		return
	}
	wd := workingDir(rows)
	r := newLockResolver(wd)
	for i := range spans {
		attrs := spans[i].Attributes
		name, ok := attrs["skill.name"].(string)
		if !ok || name == "" {
			continue
		}
		loadPath, _ := attrs["skill.load_path"].(string)
		if loadPath == "" {
			continue // no evidence — nothing to prove against
		}

		// Transcript-pinned: the RECORDED path (before any symlink
		// resolution) is already inside qvr's immutable store — registry,
		// skill, and short sha are in the bytes the agent wrote, so this is
		// proof ACROSS time (codex/openclaw record resolved paths). When the
		// current lock pin matches, the richer lock fields ride along.
		abs := absLoadPath(loadPath, wd)
		if pathReg, _, sha := storeWorktreeIdentity(abs); sha != "" {
			applyPathIdentity(attrs, r.lookup(name), pathReg, sha)
			continue
		}

		// Symlink-origin evidence from here down: prefer the ingest-time
		// snapshot when one exists — it froze the proof while the symlink
		// still pointed where it did at run time.
		if e := snap[name]; e != nil {
			applyIdentity(attrs, e)
			continue
		}

		// No snapshot: resolve the symlink now (derive-time truth — correct
		// for near-live ingest, the case that then gets snapshotted).
		real := resolveLoadPath(loadPath, wd)
		if pathReg, _, sha := storeWorktreeIdentity(real); sha != "" {
			applyPathIdentity(attrs, r.lookup(name), pathReg, sha)
			continue
		}
		// Non-store evidence: assert identity only when the loaded file
		// resolves into the lock entry's worktree; otherwise the loaded copy
		// is provably not the locked one.
		if entry := r.lookup(name); entry != nil && loadedFromEntryWorktree(loadPath, wd, entry) {
			applyIdentity(attrs, entry)
		}
	}
}

// applyPathIdentity stamps identity proven by a store path: the full lock
// fields when the current pin matches the path's sha, else the minimal
// path-derived identity (version = short sha — still verified).
func applyPathIdentity(attrs map[string]any, entry *model.LockEntry, pathReg, sha string) {
	if entry != nil && strings.HasPrefix(entry.Commit, sha) {
		applyIdentity(attrs, entry)
		return
	}
	attrs["skill.registry"] = pathReg
	attrs["skill.commit"] = sha
	attrs["skill.version"] = sha // version presence = verified
}

// HarvestVerifiedIdentities collects the proven identity per skill from
// enriched spans — the rows persistDerivation freezes as the session's
// snapshot on first ingest. Only verified spans (skill.version present)
// contribute; the snapshot never records guesses.
func HarvestVerifiedIdentities(spans []Span) map[string]*model.LockEntry {
	out := map[string]*model.LockEntry{}
	str := func(m map[string]any, k string) string {
		s, _ := m[k].(string)
		return s
	}
	for i := range spans {
		attrs := spans[i].Attributes
		name := str(attrs, "skill.name")
		if name == "" || str(attrs, "skill.version") == "" {
			continue
		}
		if _, seen := out[name]; seen {
			continue
		}
		out[name] = &model.LockEntry{
			Name:        name,
			Registry:    str(attrs, "skill.registry"),
			Ref:         str(attrs, "skill.version"),
			Commit:      str(attrs, "skill.commit"),
			SubtreeHash: str(attrs, "skill.subtree_hash"),
			Source:      str(attrs, "skill.source"),
			Canonical:   str(attrs, "skill.canonical"),
		}
	}
	return out
}

// absLoadPath absolutizes a recorded load path against the session's working
// directory WITHOUT resolving symlinks — the recorded bytes, locatable.
func absLoadPath(loadPath, workingDir string) string {
	if !filepath.IsAbs(loadPath) && workingDir != "" {
		return filepath.Join(workingDir, loadPath)
	}
	return loadPath
}

// storeWorktreePathRe parses a resolved path inside qvr's immutable store:
// …/.quiver/worktrees/<registry>/<skill>/<sha7>/…
var storeWorktreePathRe = regexp.MustCompile(`\.quiver/worktrees/([^/]+)/([^/]+)/([0-9a-f]{7})(?:/|$)`)

// storeWorktreeIdentity extracts (registry, skill, sha7) from a resolved
// store path, or zero values when the path is not in the store.
func storeWorktreeIdentity(path string) (registry, skill, sha string) {
	m := storeWorktreePathRe.FindStringSubmatch(path)
	if m == nil {
		return "", "", ""
	}
	return m[1], m[2], m[3]
}

// resolveLoadPath absolutizes a recorded load path against the session's
// working directory and resolves symlinks best-effort.
func resolveLoadPath(loadPath, workingDir string) string {
	abs := loadPath
	if !filepath.IsAbs(abs) && workingDir != "" {
		abs = filepath.Join(workingDir, abs)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

// loadedFromEntryWorktree reports whether loadPath — the file the agent actually
// referenced — resolves into the managed worktree that entry pins. loadPath may
// be an absolute worktree path or a relative agent-dir symlink; symlinks are
// resolved best-effort and the result is checked for containment under the
// entry's worktree root. A miss (eject, shadowing copy, drifted sha, or a path
// that no longer resolves) returns false, so identity is asserted only when the
// bytes the agent loaded are provably the locked ones (#149).
func loadedFromEntryWorktree(loadPath, workingDir string, e *model.LockEntry) bool {
	if loadPath == "" || e.Commit == "" {
		return false
	}
	abs := loadPath
	if !filepath.IsAbs(abs) && workingDir != "" {
		abs = filepath.Join(workingDir, abs)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	root := reg.WorktreePath(e.Registry, e.Name, reg.ShortSHA(e.Commit))
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, "..")
}

// applyIdentity writes the non-empty identity fields of entry onto attrs.
// skill.version is ALWAYS set (falling back to the short pinned SHA when the
// entry has no ref) because its presence is the verification signal — callers
// only invoke this after the load-path proof has passed.
func applyIdentity(attrs map[string]any, e *model.LockEntry) {
	set := func(k, v string) {
		if v != "" {
			attrs[k] = v
		}
	}
	set("skill.registry", e.Registry)
	version := e.Ref
	if version == "" {
		version = reg.ShortSHA(e.Commit)
	}
	set("skill.version", version)
	set("skill.commit", e.Commit)
	set("skill.source", e.Source)
	set("skill.subtree_hash", e.SubtreeHash)
	set("skill.canonical", e.Canonical)
}

// workingDir returns the first non-empty working directory across a session's
// rows. All rows in a session share the same cwd (the hook payload's), so the
// first one is authoritative; "" when no row recorded one.
func workingDir(rows []*ops.RawTrace) string {
	for _, r := range rows {
		if r.WorkingDirectory != "" {
			return r.WorkingDirectory
		}
	}
	return ""
}

// lockResolver maps a skill name to its lock entry, consulting the project
// lock first and the user-global lock as a fallback. Both locks are read once,
// lazily, and cached for the lifetime of one enrichment pass.
type lockResolver struct {
	workingDir string

	project       *model.LockFile
	projectLoaded bool
	global        *model.LockFile
	globalLoaded  bool
}

func newLockResolver(workingDir string) *lockResolver {
	return &lockResolver{workingDir: workingDir}
}

// lookup returns the lock entry for a skill name, or nil if neither lock has
// it. A failed/missing lock read is treated as "no entry" (ReadLockFile
// returns an empty lock for a missing file), so enrichment degrades to a no-op
// rather than failing derivation.
func (r *lockResolver) lookup(name string) *model.LockEntry {
	if r.workingDir != "" {
		if !r.projectLoaded {
			r.project = readLock(model.DefaultLockPath(r.workingDir, "", false))
			r.projectLoaded = true
		}
		if e := getEntry(r.project, name); e != nil {
			return e
		}
	}
	if !r.globalLoaded {
		r.global = readLock(model.DefaultLockPath("", config.Dir(), true))
		r.globalLoaded = true
	}
	return getEntry(r.global, name)
}

func readLock(path string) *model.LockFile {
	l, err := model.ReadLockFile(path)
	if err != nil {
		return nil
	}
	return l
}

func getEntry(l *model.LockFile, name string) *model.LockEntry {
	if l == nil {
		return nil
	}
	e, err := l.Get(name)
	if err != nil {
		return nil
	}
	return e
}
