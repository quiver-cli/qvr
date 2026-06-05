package derive

import (
	"path/filepath"
	"strings"

	"github.com/quiver-cli/qvr/internal/config"
	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/ops"
	reg "github.com/quiver-cli/qvr/internal/registry"
)

// EnrichSkillIdentity promotes full skill identity onto SKILL/TOOL spans that
// carry only a bare skill.name, by resolving that name against the calling
// project's qvr.lock (falling back to the user-global lock). The lockfile
// already pins each skill to registry + commit + ref + subtreeHash, so two
// registries that both ship "code-review", or two pinned versions of the same
// skill across projects, become distinguishable in logs/spans/UI/OTLP without
// scraping raw command strings (issue #146).
//
// It is intentionally a SEPARATE pass from DeriveSession, not folded into the
// derivers: a Deriver must be PURE (same rows in → same spans out), and the
// lockfile is external state not carried in the rows. Enrichment is applied at
// the persistence/display boundaries (capture + `qvr audit spans`), where
// reflecting the current pin is what callers want. Spans for skills not found
// in any lock (built-in agent skills, skills installed by another tool) are
// left with skill.name only — identity is never fabricated.
//
// Attributes added when the corresponding lock field is non-empty:
//
//	skill.registry     — the registry the skill came from (e.g. "raks")
//	skill.version      — the requested ref (branch/tag, e.g. "v0.2.0")
//	skill.commit       — the pinned git SHA at the entry's HEAD
//	skill.source       — the fetch coordinate (git URL or local path)
//	skill.subtree_hash — canonical content hash of the installed subtree
//	skill.canonical    — upstream skill name when installed under an alias
//	skill.verified     — bool: whether the asserted identity is PROVEN to be the
//	                     artifact the agent actually loaded (see below)
//
// Identity is name-keyed only as a last resort. When a span records the file
// path the agent actually loaded (codex captures it as skill.load_path), the
// path wins over the name: lock identity is asserted only when that path
// resolves into the lock entry's managed worktree, and the span is marked
// skill.verified=true. A path that doesn't match — a global eject shadowing the
// project install, a copy from another registry, a drifted sha — gets
// skill.name only and skill.verified=false; qvr never attests a
// registry+commit+subtree_hash the agent didn't run. When NO load path is known
// (claude's Skill tool call carries only {"skill":"<name>"}), the lock is the
// best available guess, so it is attached but flagged skill.verified=false
// rather than presented as authoritative (issue #149).
func EnrichSkillIdentity(spans []Span, rows []*ops.RawTrace) {
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
		entry := r.lookup(name)
		loadPath, _ := attrs["skill.load_path"].(string)

		switch {
		case loadPath != "":
			// The load path is concrete evidence: trust it over the name. Only
			// assert identity when the loaded file resolves into this entry's
			// worktree; otherwise the loaded copy is provably not the locked one.
			if entry != nil && loadedFromEntryWorktree(loadPath, wd, entry) {
				applyIdentity(attrs, entry)
				attrs["skill.verified"] = true
			} else {
				attrs["skill.verified"] = false
			}
		case entry != nil:
			// No load path (e.g. claude). Attach the lock's identity as a guess
			// but never claim it is the proven-loaded artifact.
			applyIdentity(attrs, entry)
			attrs["skill.verified"] = false
		}
	}
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
func applyIdentity(attrs map[string]any, e *model.LockEntry) {
	set := func(k, v string) {
		if v != "" {
			attrs[k] = v
		}
	}
	set("skill.registry", e.Registry)
	set("skill.version", e.Ref)
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
