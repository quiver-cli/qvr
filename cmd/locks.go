package cmd

import (
	"fmt"
	"os"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/model"
)

// scopedLock pairs a loaded lock file with its scope label ("project" or
// "global"). Inspection commands use the label as a column / section heading
// when --all is set so users can see which lock a given entry came from.
type scopedLock struct {
	Scope string
	Lock  *model.LockFile
}

// loadScopedLocks resolves the (global, all) flag pair to the lock files
// the caller should operate on. Conventions:
//
//   - all=false, global=false → project lock only.
//   - all=false, global=true  → global lock only.
//   - all=true                → both, project first, then global.
//
// Missing lock files (project never installed into; global never used) come
// back as empty LockFiles rather than errors so callers can render an
// "(empty)" section instead of bailing.
func loadScopedLocks(projectRoot string, global, all bool) ([]scopedLock, error) {
	if all && global {
		return nil, fmt.Errorf("--global and --all are mutually exclusive")
	}
	quiverHome := config.Dir()
	if all {
		projectPath := model.DefaultLockPath(projectRoot, quiverHome, false)
		project, err := model.ReadLockFile(projectPath)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("read project lock: %w", err)
		}
		if project == nil {
			project = model.NewLockFile(projectPath)
		}
		globalPath := model.DefaultLockPath(projectRoot, quiverHome, true)
		globalLock, err := model.ReadLockFile(globalPath)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("read global lock: %w", err)
		}
		if globalLock == nil {
			globalLock = model.NewLockFile(globalPath)
		}
		return []scopedLock{
			{Scope: "project", Lock: project},
			{Scope: "global", Lock: globalLock},
		}, nil
	}
	lockPath := model.DefaultLockPath(projectRoot, quiverHome, global)
	lock, err := model.ReadLockFile(lockPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read lock: %w", err)
	}
	if lock == nil {
		lock = model.NewLockFile(lockPath)
	}
	scope := "project"
	if global {
		scope = "global"
	}
	return []scopedLock{{Scope: scope, Lock: lock}}, nil
}

// loadScopedLocksLenient mirrors loadScopedLocks but never fails on a bad
// lock file: an unreadable lock (e.g. an old schema version) becomes an empty
// lock plus a warning. The dashboard uses this — one stale qvr.lock anywhere
// must not take down pages whose real data (sessions, analytics) doesn't even
// need the lock. CLI commands keep the strict loader so the regeneration hint
// in the error reaches the user as a failure they must act on.
func loadScopedLocksLenient(projectRoot string, global, all bool) ([]scopedLock, []string) {
	quiverHome := config.Dir()
	if all {
		project, warn1 := readLockOrWarn(model.DefaultLockPath(projectRoot, quiverHome, false), "project")
		globalLock, warn2 := readLockOrWarn(model.DefaultLockPath(projectRoot, quiverHome, true), "global")
		return []scopedLock{
			{Scope: "project", Lock: project},
			{Scope: "global", Lock: globalLock},
		}, compactWarnings(warn1, warn2)
	}
	scope := "project"
	if global {
		scope = "global"
	}
	lock, warn := readLockOrWarn(model.DefaultLockPath(projectRoot, quiverHome, global), scope)
	return []scopedLock{{Scope: scope, Lock: lock}}, compactWarnings(warn)
}

// readLockOrWarn reads one lock leniently: a missing file is a normal empty
// lock; an unreadable one is an empty lock plus a warning naming the scope.
func readLockOrWarn(path, scope string) (*model.LockFile, string) {
	lock, err := model.ReadLockFile(path)
	if err != nil && !os.IsNotExist(err) {
		return model.NewLockFile(path), fmt.Sprintf("%s lock skipped: %v", scope, err)
	}
	if lock == nil {
		lock = model.NewLockFile(path)
	}
	return lock, ""
}

// compactWarnings drops empty entries.
func compactWarnings(warns ...string) []string {
	var out []string
	for _, w := range warns {
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

// findEntryAcrossLocks looks up name in each of locks and returns the first
// match alongside its scope. Returns an "ambiguous" error when name resolves
// in more than one lock — the caller has to drop --all and pick a scope.
// Returns model.ErrLockSkillMissing-wrapped when no lock contains the entry.
func findEntryAcrossLocks(name string, locks []scopedLock) (*model.LockEntry, scopedLock, error) {
	var hits []scopedLock
	var first *model.LockEntry
	for _, s := range locks {
		if s.Lock == nil {
			continue
		}
		entry, err := s.Lock.Get(name)
		if err != nil {
			continue
		}
		if first == nil {
			first = entry
		}
		hits = append(hits, s)
	}
	if len(hits) == 0 {
		return nil, scopedLock{}, fmt.Errorf("%w: skill %q not found in any lock", model.ErrLockSkillMissing, name)
	}
	if len(hits) > 1 {
		return nil, scopedLock{}, fmt.Errorf("skill %q exists in both project and global locks — pass --global to disambiguate", name)
	}
	return first, hits[0], nil
}
