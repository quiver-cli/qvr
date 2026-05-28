package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/registry"
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
		if entry.Source == "link" {
			// Link installs don't have a worktree to restore — just keep
			// the symlink wired up.
			if err := r.fixSymlinks(entry, projectRoot, global, opts, res); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", entry.Name, err))
			}
			continue
		}

		needsRestore := entry.Worktree == ""
		if !needsRestore {
			if _, err := os.Stat(entry.Worktree); err != nil {
				needsRestore = true
			}
		}
		if needsRestore && r.Installer != nil && !opts.DryRun {
			ref := entry.Name
			if entry.Ref != "" {
				ref = entry.Name + "@" + entry.Ref
			}
			if _, err := r.Installer.Install(InstallRequest{
				Skill:       ref,
				Targets:     entry.Targets,
				Global:      global,
				ProjectRoot: projectRoot,
			}); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("install %s: %v", entry.Name, err))
				continue
			}
			res.Installed = append(res.Installed, entry.Name)
			// Install rewrote the lock — pick up the fresh entry so the
			// symlink pass uses the new Worktree path.
			if fresh, err := lock.Get(entry.Name); err == nil {
				entry = fresh
			}
		} else if needsRestore && opts.DryRun {
			res.Installed = append(res.Installed, entry.Name+" (would install)")
			continue
		}

		if err := r.fixSymlinks(entry, projectRoot, global, opts, res); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", entry.Name, err))
		}
	}
	return nil
}

// fixSymlinks ensures every target's symlink points at EffectiveTarget(entry).
// CreateSymlink is idempotent: an existing matching link is a no-op, a
// mismatching one gets replaced. Per-target failures are appended to
// res.Errors so the rest of the targets still get a chance.
func (r *Reconciler) fixSymlinks(entry *model.LockEntry, projectRoot string, global bool, opts ReconcileOptions, res *ReconcileResult) error {
	target := EffectiveTarget(entry)
	if target == "" {
		return nil // nothing to link (e.g. fresh entry where install just failed)
	}
	for _, t := range entry.Targets {
		linkPath, err := ResolveTargetPath(t, entry.Name, projectRoot, global)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s/%s: %v", entry.Name, t, err))
			continue
		}
		if opts.DryRun {
			if existing, err := os.Lstat(linkPath); err != nil || existing.Mode()&os.ModeSymlink == 0 {
				res.SymlinksFixed = append(res.SymlinksFixed, linkPath+" (would create)")
			}
			continue
		}
		if err := CreateSymlink(linkPath, target); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("link %s/%s: %v", entry.Name, t, err))
			continue
		}
		res.SymlinksFixed = append(res.SymlinksFixed, linkPath)
	}
	return nil
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

	managedPrefixes := managedRoots(projectRoot)

	for _, targetName := range model.TargetNames() {
		dir, ok := agentDir(targetName, projectRoot, global)
		if !ok {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			if !os.IsNotExist(err) {
				res.Errors = append(res.Errors, fmt.Sprintf("scan %s: %v", dir, err))
			}
			continue
		}
		for _, dirent := range entries {
			full := filepath.Join(dir, dirent.Name())
			info, err := os.Lstat(full)
			if err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("stat %s: %v", full, err))
				continue
			}
			if info.Mode()&os.ModeSymlink == 0 {
				// Hand-placed file/dir — never our business.
				continue
			}
			if _, ok := tracked[normalize(full)]; ok {
				continue
			}
			resolved, rerr := os.Readlink(full)
			if rerr != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("readlink %s: %v", full, rerr))
				continue
			}
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(filepath.Dir(full), resolved)
			}
			if !isManaged(resolved, managedPrefixes) {
				// Symlink points outside the qvr-managed scope (e.g. into
				// /etc/passwd or the user's own dev dir). The strict rule
				// is to leave it alone and surface it.
				res.Skipped = append(res.Skipped, fmt.Sprintf("%s -> %s (target outside qvr scope)", full, resolved))
				continue
			}
			if opts.KeepUntracked {
				res.Skipped = append(res.Skipped, full+" (managed but --keep-untracked)")
				continue
			}
			if opts.DryRun {
				res.Removed = append(res.Removed, full+" (would remove)")
				continue
			}
			if err := RemoveSymlink(full); err != nil {
				// RemoveSymlink only errors when the path isn't a symlink
				// (we already checked) or the unlink itself fails. Either
				// way, record and move on.
				res.Errors = append(res.Errors, fmt.Sprintf("remove %s: %v", full, err))
				continue
			}
			res.Removed = append(res.Removed, full)
		}
	}
	return nil
}

// managedRoots returns the absolute paths whose subtree is fair game for
// strict removal: the shared worktrees cache and the project's vendor dir.
// Anything else is "user territory" and stays untouched.
func managedRoots(projectRoot string) []string {
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

// isManaged reports whether resolved lives under any managed prefix. We
// compare on the cleaned absolute form so trailing slashes or `..` in the
// symlink content can't sneak past the prefix check.
func isManaged(resolved string, prefixes []string) bool {
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
