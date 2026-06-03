package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/model"
)

// ProjectsFileName is the on-disk filename for the reachability registry.
// Lives next to config.yaml at the QUIVER_HOME root.
const ProjectsFileName = "projects.json"

// ProjectRecord tracks one project's lock file so cache reachability can be
// computed without walking the filesystem. LastSeen is bumped every time the
// project's lock is touched by a qvr command.
type ProjectRecord struct {
	LockPath string    `json:"lockPath"`
	LastSeen time.Time `json:"lastSeen"`
}

// ProjectsFile is the deserialised shape of ~/.quiver/projects.json. The map
// keys are absolute project roots — the same string DefaultLockPath would
// compute from a project's qvr.lock parent directory.
type ProjectsFile struct {
	Projects map[string]ProjectRecord `json:"projects"`
}

// projectsMu guards the read-modify-write of the projects file across
// goroutines in the same process. Cross-process safety is handled by the
// flock on qvr.lock — the projects file is only touched as part of a
// command that's already holding its lock — but the in-process mutex is
// still needed because the same binary may run with multiple goroutines
// touching different projects in tests.
var projectsMu sync.Mutex

// ProjectsPath returns the absolute path to the projects file.
func ProjectsPath() string {
	return filepath.Join(config.Dir(), ProjectsFileName)
}

// ReadProjects loads the projects file. Returns an empty ProjectsFile when
// the file does not exist — the expected state before the first qvr add.
func ReadProjects() (*ProjectsFile, error) {
	data, err := os.ReadFile(ProjectsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &ProjectsFile{Projects: map[string]ProjectRecord{}}, nil
		}
		return nil, fmt.Errorf("read projects: %w", err)
	}
	pf := &ProjectsFile{}
	if len(data) == 0 {
		pf.Projects = map[string]ProjectRecord{}
		return pf, nil
	}
	if err := json.Unmarshal(data, pf); err != nil {
		return nil, fmt.Errorf("parse projects: %w", err)
	}
	if pf.Projects == nil {
		pf.Projects = map[string]ProjectRecord{}
	}
	return pf, nil
}

// WriteProjects persists the file atomically (tmp + rename).
func WriteProjects(pf *ProjectsFile) error {
	if pf == nil {
		return errors.New("projects: nil file")
	}
	if pf.Projects == nil {
		pf.Projects = map[string]ProjectRecord{}
	}
	path := ProjectsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create projects dir: %w", err)
	}
	data, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal projects: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write projects tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename projects: %w", err)
	}
	return nil
}

// TouchProject upserts a record for the given lock file, bumping LastSeen to
// now. Best-effort — a write failure is non-fatal because the projects file
// is purely an optimisation for `qvr cache prune`. The user-global lock at
// ~/.quiver/qvr.lock is always considered reachable and does not need to be
// touched (Reachable walks it unconditionally).
func TouchProject(lockPath string) {
	if lockPath == "" {
		return
	}
	abs, err := filepath.Abs(lockPath)
	if err != nil {
		return
	}
	// Skip the global lock — it's always reachable; recording it would just
	// be noise.
	if filepath.Dir(abs) == config.Dir() {
		return
	}
	projectsMu.Lock()
	defer projectsMu.Unlock()
	pf, err := ReadProjects()
	if err != nil {
		return
	}
	pf.Projects[abs] = ProjectRecord{
		LockPath: abs,
		LastSeen: time.Now().UTC(),
	}
	_ = WriteProjects(pf)
}

// ForgetProject drops a record from the projects file. Used by `qvr cache
// prune` when a project's lock file has vanished so subsequent reachability
// passes don't keep trying to read a dead path.
func ForgetProject(lockPath string) {
	if lockPath == "" {
		return
	}
	abs, err := filepath.Abs(lockPath)
	if err != nil {
		return
	}
	projectsMu.Lock()
	defer projectsMu.Unlock()
	pf, err := ReadProjects()
	if err != nil {
		return
	}
	if _, ok := pf.Projects[abs]; !ok {
		return
	}
	delete(pf.Projects, abs)
	_ = WriteProjects(pf)
}

// ReachabilityResult is the per-call output of Reachable: every worktree path
// referenced by any known lock, plus a list of projects whose lock has gone
// missing (so the caller can decide whether to ForgetProject them).
type ReachabilityResult struct {
	// Worktrees is the set of absolute worktree paths referenced by any
	// reachable lock entry. Empty paths and link installs are skipped.
	Worktrees map[string]struct{}
	// MissingProjects lists project lock paths that were recorded in
	// projects.json but no longer exist on disk. Useful as a hint to
	// ForgetProject during prune.
	MissingProjects []string
}

// Reachable walks every project lock recorded in projects.json plus the
// user-global lock at ~/.quiver/qvr.lock and returns the set of worktree
// paths still referenced by any entry. A `qvr cache prune` walks the
// worktrees root and considers anything not in the set an orphan.
//
// A project whose lock file no longer exists (deleted/moved project dir)
// contributes nothing but is recorded in MissingProjects so the caller can
// prune it from the registry on its next sweep.
func Reachable() (*ReachabilityResult, error) {
	res := &ReachabilityResult{Worktrees: map[string]struct{}{}}

	// The global lock is always reachable, no projects.json entry required.
	globalLock := filepath.Join(config.Dir(), model.LockFileName)
	addLockWorktrees(globalLock, res.Worktrees)

	pf, err := ReadProjects()
	if err != nil {
		return res, err
	}
	for _, rec := range pf.Projects {
		if _, err := os.Stat(rec.LockPath); err != nil {
			res.MissingProjects = append(res.MissingProjects, rec.LockPath)
			continue
		}
		addLockWorktrees(rec.LockPath, res.Worktrees)
	}
	sort.Strings(res.MissingProjects)
	return res, nil
}

// addLockWorktrees opens lockPath, decodes the entries, and adds the derived
// worktree path (via WorktreePath) for each non-link entry to set. Errors are
// swallowed — best-effort; a malformed lock just contributes nothing rather
// than blocking the whole reachability pass.
func addLockWorktrees(lockPath string, set map[string]struct{}) {
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return
	}
	for _, e := range lock.Entries() {
		if e.IsLink() || e.Registry == "" {
			continue
		}
		// WorktreePathForEntry honors --as aliases (canonical name) and the
		// install-commit pin — using e.Name/e.Commit directly here treated every
		// aliased multi-version worktree as an orphan, so `qvr cache prune`
		// deleted referenced installs (issue #158).
		path := WorktreePathForEntry(e)
		if path != "" {
			set[path] = struct{}{}
		}
	}
}
