package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/registry"
)

// Reconciler drives `qvr sync`: re-materializes anything in the lock that's
// missing on disk and strict-removes any symlink under a managed agent
// directory (.claude/skills/, .cursor/rules/, …) that points into a
// qvr-owned cache but has no corresponding lock entry.
//
// The "managed cache" allowlist is fixed by design: a symlink is only a
// removal candidate when its resolved target lives under either
// ~/.quiver/worktrees/ or <project>/.qvr/. Anything else (hand-placed
// directories, symlinks into /etc, hardlinks, dangling links pointing
// nowhere we recognise) is left alone and surfaced to the caller as a
// Skipped entry so the user sees what we noticed without us touching it.
type Reconciler struct {
	Installer *Installer
}

// NewReconciler wraps an Installer. The installer owns the bare-clone +
// worktree machinery; the reconciler delegates restoration to it and then
// runs the strict-removal pass on top.
func NewReconciler(in *Installer) *Reconciler {
	return &Reconciler{Installer: in}
}

// ReconcileOptions tunes the strict pass.
type ReconcileOptions struct {
	// DryRun reports what would change without touching the filesystem.
	DryRun bool
	// RequireSigned refuses materializing unsigned locked entries.
	RequireSigned bool
	// TrustedAuthorsByRegistry refuses materializing entries authored by anyone
	// outside the registry's pinned author list.
	TrustedAuthorsByRegistry map[string][]string
	// KeepUntracked downgrades orphan removal to a warning. Used by projects
	// that mix hand-managed skills with qvr-managed ones and want sync to
	// leave the manual entries alone.
	KeepUntracked bool
}

// ReconcileResult is the structured output of a sync run. Fields are sorted
// for deterministic test assertions and stable JSON output.
type ReconcileResult struct {
	Installed     []string `json:"installed"`     // skills (re-)materialized from cache
	SymlinksFixed []string `json:"symlinksFixed"` // symlinks created or repaired against the lock
	Removed       []string `json:"removed"`       // managed orphan symlinks removed
	Skipped       []string `json:"skipped"`       // unmanaged orphans we deliberately left alone
	Errors        []string `json:"errors"`        // non-fatal errors collected during the pass
}

// Reconcile is the entry point. The lock's location decides whether the
// agent-directory walk runs against project-local or user-global dirs.
//
// Errors returned directly are fatal (couldn't read the lockfile, couldn't
// stat a managed root). Per-entry / per-symlink problems land in
// ReconcileResult.Errors so the user sees the full picture instead of the
// first failure aborting the rest.
func (r *Reconciler) Reconcile(lock *model.LockFile, projectRoot, quiverHome string, opts ReconcileOptions) (*ReconcileResult, error) {
	if lock == nil {
		return nil, errors.New("reconcile: lock file is nil")
	}
	res := &ReconcileResult{}
	global := lock.IsGlobal(quiverHome)

	if err := r.restoreFromLock(lock, projectRoot, global, opts, res); err != nil {
		return res, err
	}
	if err := r.strictRemoveOrphans(lock, projectRoot, global, opts, res); err != nil {
		return res, err
	}

	sort.Strings(res.Installed)
	sort.Strings(res.SymlinksFixed)
	sort.Strings(res.Removed)
	sort.Strings(res.Skipped)
	sort.Strings(res.Errors)
	return res, nil
}

// restoreFromLock walks every enabled lock entry and re-runs the installer
// for any whose worktree no longer exists on disk, then refreshes each
// target symlink against the expected EffectiveTarget. Disabled entries are
// skipped (they intentionally have no symlinks).
func (r *Reconciler) restoreFromLock(lock *model.LockFile, projectRoot string, global bool, opts ReconcileOptions, res *ReconcileResult) error {
	for _, entry := range lock.Entries() {
		if entry.Disabled {
			continue
		}
		if entry.IsLink() {
			// Link installs don't have a worktree to restore — just keep
			// the symlink wired up.
			if err := r.fixSymlinks(entry, projectRoot, global, opts, res); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", entry.Name, err))
			}
			continue
		}
		if entry.IsEdit() {
			// Edit-mode entries live at EditPath inside the project, not in
			// the shared cache. The shared worktree is no longer load-bearing,
			// so we don't try to (re-)install — just verify the edit dir is
			// present and refresh sibling symlinks. If EditPath is missing
			// after e.g. a teammate cloned the repo without the dir,
			// surface a clear error rather than silently overwriting with
			// shared-mode contents.
			editDir := EffectiveTarget(entry, projectRoot)
			if _, err := os.Stat(editDir); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: edit dir %s missing — re-run `qvr edit %s` to re-materialize from %s@%s",
					entry.Name, editDir, entry.Name, entry.SourceUpstream, shortHashOrFull(entry.InstallCommit)))
				continue
			}
			if err := r.fixSymlinks(entry, projectRoot, global, opts, res); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", entry.Name, err))
			}
			continue
		}

		if entry.IsLocal() {
			// Local copies (`qvr add --local`) have no git upstream, so the
			// generic registry-restore below can't rebuild them. Re-copy from
			// the recorded source path when the frozen worktree is missing; if
			// the source is gone, surface a clear error rather than silently
			// dropping the skill.
			if err := r.restoreLocal(entry, projectRoot, global, opts, res); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", entry.Name, err))
			}
			continue
		}

		entry, skip := r.restoreShared(entry, lock, projectRoot, global, opts, res)
		if skip {
			continue
		}

		if err := r.fixSymlinks(entry, projectRoot, global, opts, res); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", entry.Name, err))
		}
	}
	return nil
}

// restoreShared restores a shared (registry) entry whose worktree is missing,
// re-installing it pinned to the lock's recorded commit (the uv reproducibility
// contract). Returns the entry to link (refreshed from the lock after a real
// install) and skip=true when the caller should continue to the next entry
// without running the symlink pass (an install error, or a dry-run "would
// install" record).
func (r *Reconciler) restoreShared(entry *model.LockEntry, lock *model.LockFile, projectRoot string, global bool, opts ReconcileOptions, res *ReconcileResult) (*model.LockEntry, bool) {
	worktreePath := EntryWorktreePath(entry)
	needsRestore := worktreePath == ""
	if !needsRestore {
		if _, err := os.Stat(worktreePath); err != nil {
			needsRestore = true
		}
	}
	if needsRestore && r.Installer != nil && !opts.DryRun {
		// Aliased entries (`qvr add <skill> --as <alias>`) key the lock on
		// the alias but resolve their registry source by the canonical name;
		// preserve the alias via As so the restored lock key stays the alias.
		// Without this swap the resolver is handed the alias key (e.g.
		// "careful-v1") as a registry skill name and fails ErrSkillNotFound,
		// so sync — the documented repair path — can't restore a missing
		// aliased worktree (issue #159). Mirrors Installer.RestoreAll.
		canonicalName := entry.Name
		aliasFlag := ""
		if entry.Canonical != "" {
			canonicalName = entry.Canonical
			aliasFlag = entry.Name
		}
		ref := canonicalName
		if entry.Ref != "" {
			ref = canonicalName + "@" + entry.Ref
		}
		// uv reproducibility contract: restore the lock's recorded commit,
		// not whatever the ref label resolves to now. A teammate cloning
		// the project and running `qvr sync` gets the locked commit even if
		// upstream "main" advanced — only `qvr update` re-resolves. Pin to
		// the same SHA EntryWorktreePath keys on so the restored dir lands
		// where the symlink pass expects it.
		pin := entry.InstallCommit
		if pin == "" {
			pin = entry.Commit
		}
		if _, err := r.Installer.Install(InstallRequest{
			Skill:                    ref,
			Targets:                  entry.Targets,
			Global:                   global,
			ProjectRoot:              projectRoot,
			PinCommit:                pin,
			As:                       aliasFlag,
			RequireSigned:            opts.RequireSigned,
			TrustedAuthors:           opts.TrustedAuthorsByRegistry[entry.Registry],
			TrustedAuthorsByRegistry: opts.TrustedAuthorsByRegistry,
		}); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("install %s: %v", entry.Name, err))
			return entry, true
		}
		res.Installed = append(res.Installed, entry.Name)
		// Install rewrote the lock — pick up the fresh entry so the
		// symlink pass uses the new Worktree path.
		if fresh, err := lock.Get(entry.Name); err == nil {
			entry = fresh
		}
	} else if needsRestore && opts.DryRun {
		res.Installed = append(res.Installed, entry.Name+" (would install)")
		return entry, true
	}
	return entry, false
}

// restoreLocal reconciles a `qvr add --local` entry. Its immutable copy lives
// in a hash-keyed worktree under the LocalRegistry namespace, with no git
// upstream to re-materialize from — so when the copy is missing we rebuild it
// from the recorded Source path instead. A vanished Source is a hard error
// (the skill is unrecoverable until re-added), mirroring how a missing edit
// dir is surfaced rather than silently overwritten.
func (r *Reconciler) restoreLocal(entry *model.LockEntry, projectRoot string, global bool, opts ReconcileOptions, res *ReconcileResult) error {
	worktreePath := EntryWorktreePath(entry)
	if worktreePath != "" {
		if _, err := os.Stat(worktreePath); err == nil {
			// Copy already present — just keep the symlinks wired up.
			return r.fixSymlinks(entry, projectRoot, global, opts, res)
		}
	}
	if opts.DryRun {
		res.Installed = append(res.Installed, entry.Name+" (would re-copy from "+entry.Source+")")
		return nil
	}
	if entry.Source == "" {
		return fmt.Errorf("local copy missing and no source recorded — re-run `qvr add --local <path>`")
	}
	if _, err := os.Stat(entry.Source); err != nil {
		return fmt.Errorf("local source %s missing — re-run `qvr add --local %s` once the folder exists", entry.Source, entry.Source)
	}
	if worktreePath == "" {
		return fmt.Errorf("cannot derive worktree path for local copy — re-run `qvr add --local %s`", entry.Source)
	}
	staging := fmt.Sprintf("%s.staging.%d", worktreePath, os.Getpid())
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return fmt.Errorf("create worktrees dir: %w", err)
	}
	if err := copyDir(entry.Source, staging); err != nil {
		_ = os.RemoveAll(staging)
		return fmt.Errorf("re-copy local skill from %s: %w", entry.Source, err)
	}
	if err := os.Rename(staging, worktreePath); err != nil {
		if _, e := os.Stat(worktreePath); e == nil {
			_ = os.RemoveAll(staging)
		} else {
			_ = os.RemoveAll(staging)
			return fmt.Errorf("finalize local worktree: %w", err)
		}
	}
	setSubtreeReadOnly(worktreePath)
	res.Installed = append(res.Installed, entry.Name)
	return r.fixSymlinks(entry, projectRoot, global, opts, res)
}

// fixSymlinks ensures every target's symlink points at EffectiveTarget(entry).
// CreateSymlink is idempotent: an existing matching link is a no-op, a
// mismatching one gets replaced. Per-target failures are appended to
// res.Errors so the rest of the targets still get a chance.
//
// Edit-mode entries are handled specially: the canonical target dir IS the
// edit copy (a real directory), so we never try to symlink over it. Sibling
// targets get repointed at the canonical via CreateSymlink as usual.
func (r *Reconciler) fixSymlinks(entry *model.LockEntry, projectRoot string, global bool, opts ReconcileOptions, res *ReconcileResult) error {
	// A consumed root-layout skill links to a sanitized view (no .git), not the
	// worktree root (issue #154). AgentLinkTarget names it; materialize it on a
	// real (non-dry-run) reconcile so the symlink resolves. Dry-run stays
	// side-effect-free.
	target := AgentLinkTarget(entry, projectRoot)
	if target == "" {
		return nil // nothing to link (e.g. fresh entry where install just failed)
	}
	if !opts.DryRun && isConsumedRootLayout(entry) {
		if _, verr := MaterializeAgentView(entry, projectRoot); verr != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: agent view: %v", entry.Name, verr))
			return nil
		}
	}
	// For edit-mode entries the canonical EditPath is itself a real directory
	// that must not be replaced with a self-referential symlink. Identify the
	// canonical absolute path so we can skip it in the per-target loop.
	var canonicalAbs string
	if entry.IsEdit() {
		if abs, err := filepath.Abs(target); err == nil {
			canonicalAbs = normalize(abs)
		}
	}
	for _, t := range entry.Targets {
		r.fixOneSymlink(entry, t, target, canonicalAbs, projectRoot, global, opts, res)
	}
	return nil
}

// fixOneSymlink ensures a single target's symlink points at target. It skips the
// canonical edit dir (the real source of truth, never a self-link), records a
// "would create" under --dry-run, and otherwise creates/repoints the link —
// staying silent in res.SymlinksFixed when the link is already correct (issue
// #79). Per-target failures are appended to res.Errors so siblings still run.
func (r *Reconciler) fixOneSymlink(entry *model.LockEntry, t, target, canonicalAbs, projectRoot string, global bool, opts ReconcileOptions, res *ReconcileResult) {
	linkPath, err := ResolveTargetPath(t, entry.Name, projectRoot, global)
	if err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("%s/%s: %v", entry.Name, t, err))
		return
	}
	if canonicalAbs != "" {
		if abs, err := filepath.Abs(linkPath); err == nil && normalize(abs) == canonicalAbs {
			// This is the canonical edit dir itself — already the real
			// source of truth, nothing to symlink.
			return
		}
	}
	if opts.DryRun {
		if existing, err := os.Lstat(linkPath); err != nil || existing.Mode()&os.ModeSymlink == 0 {
			res.SymlinksFixed = append(res.SymlinksFixed, linkPath+" (would create)")
		}
		return
	}
	// Suppress the "Linked …" report when the symlink already points at
	// the right target — sync should be silent on no-state-change reruns
	// (issue #79). Existing-but-wrong / missing links still go through
	// CreateSymlink below and DO get reported.
	alreadyCorrect := false
	if absTarget, aerr := filepath.Abs(target); aerr == nil {
		if existing, lerr := os.Lstat(linkPath); lerr == nil && existing.Mode()&os.ModeSymlink != 0 {
			if cur, rerr := os.Readlink(linkPath); rerr == nil && cur == absTarget {
				alreadyCorrect = true
			}
		}
	}
	if err := CreateSymlink(linkPath, target); err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("link %s/%s: %v", entry.Name, t, err))
		return
	}
	if !alreadyCorrect {
		res.SymlinksFixed = append(res.SymlinksFixed, linkPath)
	}
}

// strictRemoveOrphans walks each agent target's directory (project-local
// or global, matching the lock's location) and removes any symlink whose
// target lives under a qvr-managed root but doesn't correspond to a lock
// entry. Symlinks pointing outside the managed roots are recorded in
// res.Skipped — the user sees what we noticed but we never touch them.
//
// Non-symlink entries (regular files, hand-placed directories) are
// ignored entirely. The strict guarantee is "the lock is the truth for
// what qvr manages," not "qvr owns the entire agent directory."
func (r *Reconciler) strictRemoveOrphans(lock *model.LockFile, projectRoot string, global bool, opts ReconcileOptions, res *ReconcileResult) error {
	tracked := make(map[string]struct{}, len(lock.Skills))
	for _, e := range lock.Entries() {
		if e.Disabled {
			continue
		}
		for _, t := range e.Targets {
			if path, err := ResolveTargetPath(t, e.Name, projectRoot, global); err == nil {
				tracked[normalize(path)] = struct{}{}
			}
		}
	}

	managedPrefixes := ManagedRoots(projectRoot)

	// Many targets legitimately share the same skills directory (the AGENTS.md
	// `.agents/skills` convention, or `.claude/skills` shared by claude and
	// xcode-claude). Scan each unique directory once so an orphan symlink under
	// a shared dir isn't reported (or removed) multiple times.
	scanned := make(map[string]struct{}, len(model.Targets))
	for _, targetName := range model.TargetNames() {
		dir, ok := agentDir(targetName, projectRoot, global)
		if !ok {
			continue
		}
		if _, dup := scanned[normalize(dir)]; dup {
			continue
		}
		scanned[normalize(dir)] = struct{}{}
		entries, err := os.ReadDir(dir)
		if err != nil {
			if !os.IsNotExist(err) {
				res.Errors = append(res.Errors, fmt.Sprintf("scan %s: %v", dir, err))
			}
			continue
		}
		for _, dirent := range entries {
			sweepOrphanDirent(dir, dirent, tracked, managedPrefixes, opts, res)
		}
	}
	return nil
}

// sweepOrphanDirent classifies a single agent-dir entry under strict removal:
// non-symlinks and tracked/out-of-scope symlinks are left alone (the latter
// surfaced in res.Skipped), and a managed but untracked symlink is removed
// (or recorded under --dry-run / --keep-untracked).
func sweepOrphanDirent(dir string, dirent os.DirEntry, tracked map[string]struct{}, managedPrefixes []string, opts ReconcileOptions, res *ReconcileResult) {
	full := filepath.Join(dir, dirent.Name())
	info, err := os.Lstat(full)
	if err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("stat %s: %v", full, err))
		return
	}
	if info.Mode()&os.ModeSymlink == 0 {
		// Hand-placed file/dir — never our business.
		return
	}
	if _, ok := tracked[normalize(full)]; ok {
		return
	}
	resolved, rerr := os.Readlink(full)
	if rerr != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("readlink %s: %v", full, rerr))
		return
	}
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(filepath.Dir(full), resolved)
	}
	if !IsManaged(resolved, managedPrefixes) {
		// Symlink points outside the qvr-managed scope (e.g. into
		// /etc/passwd or the user's own dev dir). The strict rule
		// is to leave it alone and surface it.
		res.Skipped = append(res.Skipped, fmt.Sprintf("%s -> %s (target outside qvr scope)", full, resolved))
		return
	}
	if opts.KeepUntracked {
		res.Skipped = append(res.Skipped, full+" (managed but --keep-untracked)")
		return
	}
	if opts.DryRun {
		res.Removed = append(res.Removed, full+" (would remove)")
		return
	}
	if err := RemoveSymlink(full); err != nil {
		// RemoveSymlink only errors when the path isn't a symlink
		// (we already checked) or the unlink itself fails. Either
		// way, record and move on.
		res.Errors = append(res.Errors, fmt.Sprintf("remove %s: %v", full, err))
		return
	}
	res.Removed = append(res.Removed, full)
}

// ManagedRoots returns the absolute paths whose subtree is fair game for
// strict removal: the shared worktrees cache and the project's vendor dir.
// Anything else is "user territory" and stays untouched.
//
// Exported so `qvr doctor` can apply the same policy as `qvr sync` for
// agent-dir symlinks whose target sits outside qvr-managed scope (issue
// #68) — same helper, no drift between commands.
func ManagedRoots(projectRoot string) []string {
	roots := []string{registry.WorktreesRoot()}
	if projectRoot != "" {
		roots = append(roots, filepath.Join(projectRoot, ".qvr"))
	}
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		if abs, err := filepath.Abs(r); err == nil {
			out = append(out, normalize(abs))
		}
	}
	return out
}

// agentDir returns the on-disk directory the agent reads skills from, given
// the target name and the global/local toggle. Returns ok=false for unknown
// targets so the caller can skip without erroring.
func agentDir(targetName, projectRoot string, global bool) (string, bool) {
	t, ok := model.Targets[targetName]
	if !ok {
		return "", false
	}
	if global {
		expanded, err := expandHome(t.GlobalDir)
		if err != nil {
			return "", false
		}
		return expanded, true
	}
	return filepath.Join(projectRoot, t.LocalDir), true
}

// IsManaged reports whether resolved lives under any managed prefix. We
// compare on the cleaned absolute form so trailing slashes or `..` in the
// symlink content can't sneak past the prefix check.
//
// Exported alongside ManagedRoots so `qvr doctor` can distinguish
// `extra-symlink` (target inside ~/.quiver/, an actual orphan we'd
// remove on sync) from a benign symlink whose target is some other
// tool's territory (issue #68).
func IsManaged(resolved string, prefixes []string) bool {
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return false
	}
	abs = normalize(abs)
	for _, p := range prefixes {
		if abs == p {
			return true
		}
		if strings.HasPrefix(abs, p+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func normalize(p string) string {
	return filepath.Clean(p)
}

// shortHashOrFull returns a 7-char prefix for non-empty hashes, else the
// literal "<unknown>" so reconcile error messages stay readable when an
// entry pre-dates the InstallCommit field.
func shortHashOrFull(h string) string {
	if h == "" {
		return "<unknown>"
	}
	if len(h) >= 7 {
		return h[:7]
	}
	return h
}
