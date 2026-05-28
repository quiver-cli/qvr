package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// LockFileVersion is the current lock file schema version.
//
// v4 is the project-local pivot: per-entry Branch/Commit rename to Ref/
// ResolvedSHA, Global flag dropped (locks are distinguished by location), and
// InstallPath added so the lock records where the symlink (or vendored copy)
// lives. Older schemas are rejected at ReadLockFile time — `qvr` is
// pre-release with no external users, so the only recourse is to delete
// the old lock and reinstall.
const LockFileVersion = 4

// LockFileName is the canonical lock file name.
const LockFileName = "qvr.lock"

// MinSupportedLockFileVersion is the oldest schema this binary will read.
// v4 is a hard break — anything older is rejected outright.
const MinSupportedLockFileVersion = 4

var (
	ErrLockNotFound           = errors.New("lock file not found")
	ErrLockSkillMissing       = errors.New("skill not present in lock file")
	ErrLockVersionUnsupported = errors.New("unsupported lock file version")
)

// LockEntry records a single installed skill's filesystem and git state.
type LockEntry struct {
	Name        string    `json:"name"`
	Registry    string    `json:"registry"`
	Path        string    `json:"path"` // relative path inside the registry repo
	Ref         string    `json:"ref"`  // human label: branch or tag at install time
	ResolvedSHA string    `json:"resolvedSha"`
	Worktree    string    `json:"worktree"`    // ~/.quiver/worktrees/<reg>--<skill>--<sha7>
	InstallPath string    `json:"installPath"` // first managed agent-dir symlink (or vendor dir)
	Targets     []string  `json:"targets"`
	InstalledAt time.Time `json:"installedAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	// Source is "registry" or "link". "registry" covers everything resolved
	// through a configured source (the indexer doesn't care whether the bare
	// clone is single-skill or multi-skill). "link" is the local-dev path
	// from `qvr link <local-path>` — no worktree, LinkTarget points at the
	// live directory.
	Source string `json:"source,omitempty"`
	// RepoURL is the canonical clone URL the skill was installed from.
	// Optional — the registry config still owns the URL for source=="registry".
	RepoURL string `json:"repoURL,omitempty"`
	// LinkTarget is the absolute path of the source dir when Source == "link".
	LinkTarget string `json:"linkTarget,omitempty"`
	// Disabled hides the skill from agents without removing the worktree.
	Disabled bool `json:"disabled,omitempty"`
	// Verification carries the supply-chain provenance block.
	Verification *VerificationRecord `json:"verification,omitempty"`
}

// LockFile is the on-disk record of installed skills.
type LockFile struct {
	Version int                   `json:"version"`
	Skills  map[string]*LockEntry `json:"skills"`
	path    string                // canonical write destination — not serialized
}

// NewLockFile returns an empty lock file at the given path.
func NewLockFile(path string) *LockFile {
	return &LockFile{
		Version: LockFileVersion,
		Skills:  make(map[string]*LockEntry),
		path:    path,
	}
}

// Path returns the lock file's on-disk path.
func (l *LockFile) Path() string { return l.path }

// SetPath overrides the lock file's on-disk path.
func (l *LockFile) SetPath(p string) { l.path = p }

// IsGlobal reports whether this lock file lives at the global location
// (~/.quiver/qvr.lock vs <project>/qvr.lock). Replaces the per-entry Global
// flag — callers that need "which agent dir does this skill's symlink live
// in?" ask the lock file, not the entry.
func (l *LockFile) IsGlobal(quiverHome string) bool {
	if l.path == "" || quiverHome == "" {
		return false
	}
	return filepath.Clean(filepath.Dir(l.path)) == filepath.Clean(quiverHome)
}

// ReadLockFile loads the lock file at path. Returns an empty lock file when
// the path does not exist — this is the expected state before the first
// install.
//
// Rejects any schema version other than v4 with an error wrapping
// ErrLockVersionUnsupported. qvr is pre-release with no external users, so
// older shapes (v2, v3, legacy qvr.lock.json) are not supported — delete the
// old lock and reinstall.
func ReadLockFile(path string) (*LockFile, error) {
	l := NewLockFile(path)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return l, nil
		}
		return nil, fmt.Errorf("read lock file: %w", err)
	}
	if len(data) == 0 {
		return l, nil
	}
	// Zero out Version before Unmarshal — NewLockFile seeds it to
	// LockFileVersion, but json.Unmarshal won't reset fields that aren't
	// in the input, so we'd silently accept a missing `version` key.
	l.Version = 0
	if err := json.Unmarshal(data, l); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) && typeErr.Field == "version" {
			rawVersion := extractRawVersion(data)
			return nil, fmt.Errorf("%w: `version` must be an integer, got %s — delete the lock and reinstall",
				ErrLockVersionUnsupported, rawVersion)
		}
		return nil, fmt.Errorf("parse lock file: %w", err)
	}
	if l.Version == 0 {
		return nil, fmt.Errorf("%w: lock file missing `version` field — delete the lock and reinstall",
			ErrLockVersionUnsupported)
	}
	if l.Version < MinSupportedLockFileVersion {
		return nil, fmt.Errorf("%w: version %d predates the v%d project-local pivot — delete the lock and reinstall",
			ErrLockVersionUnsupported, l.Version, MinSupportedLockFileVersion)
	}
	if l.Version > LockFileVersion {
		return nil, fmt.Errorf("%w: version %d was written by a newer qvr (this binary writes v%d) — upgrade qvr",
			ErrLockVersionUnsupported, l.Version, LockFileVersion)
	}
	l.path = path
	if l.Skills == nil {
		l.Skills = make(map[string]*LockEntry)
	}
	return l, nil
}

// Write persists the lock file atomically: write to a temp sibling, then rename.
func (l *LockFile) Write() error {
	if l.path == "" {
		return errors.New("lock file path not set")
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return fmt.Errorf("create lock dir: %w", err)
	}
	l.Version = LockFileVersion
	if l.Skills == nil {
		l.Skills = make(map[string]*LockEntry)
	}

	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal lock file: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(l.path), ".lock-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp lock: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp lock: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp lock: %w", err)
	}
	if err := os.Rename(tmpPath, l.path); err != nil {
		cleanup()
		return fmt.Errorf("rename temp lock: %w", err)
	}
	return nil
}

// Put upserts an entry under its Name.
func (l *LockFile) Put(entry *LockEntry) {
	if l.Skills == nil {
		l.Skills = make(map[string]*LockEntry)
	}
	entry.UpdatedAt = time.Now().UTC()
	if entry.InstalledAt.IsZero() {
		entry.InstalledAt = entry.UpdatedAt
	}
	if entry.Source == "" {
		entry.Source = "registry"
	}
	l.Skills[entry.Name] = entry
}

// Get returns the entry for a skill or ErrLockSkillMissing if absent.
func (l *LockFile) Get(name string) (*LockEntry, error) {
	if l.Skills == nil {
		return nil, fmt.Errorf("%w: %s", ErrLockSkillMissing, name)
	}
	e, ok := l.Skills[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrLockSkillMissing, name)
	}
	return e, nil
}

// Remove deletes an entry. Returns ErrLockSkillMissing if not present.
func (l *LockFile) Remove(name string) error {
	if _, ok := l.Skills[name]; !ok {
		return fmt.Errorf("%w: %s", ErrLockSkillMissing, name)
	}
	delete(l.Skills, name)
	return nil
}

// Entries returns all skill entries sorted by name (stable iteration order).
func (l *LockFile) Entries() []*LockEntry {
	out := make([]*LockEntry, 0, len(l.Skills))
	for _, e := range l.Skills {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// extractRawVersion pulls the `version` field out of the raw JSON without
// caring about its type, so the friendly error message can echo what the
// user actually wrote (e.g. `"three"`).
func extractRawVersion(data []byte) string {
	var probe struct {
		Version json.RawMessage `json:"version"`
	}
	if err := json.Unmarshal(data, &probe); err != nil || len(probe.Version) == 0 {
		return "<unknown>"
	}
	return string(probe.Version)
}

// DefaultLockPath returns the lock path. The `global` arg picks the location:
// project-local writes alongside the project; global writes under the quiver
// home directory.
func DefaultLockPath(projectRoot, quiverHome string, global bool) string {
	if global {
		return filepath.Join(quiverHome, LockFileName)
	}
	return filepath.Join(projectRoot, LockFileName)
}
