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
// v3 adds VerificationRecord per entry — the supply-chain provenance block
// (subtree hash, scan/eval/signature/attestation refs, trust status) that
// later pipeline phases write into. v2 files load transparently; the bump
// happens on the next Write().
const LockFileVersion = 3

// LockFileName is the default lock file name.
const LockFileName = "qvr.lock"

// LegacyLockFileName is the pre-v0.4.6 lockfile name. ReadLockFile falls back
// to this when the canonical LockFileName is absent, and Write() removes it
// after migrating contents to the new path. The fallback is one-way: writes
// always go to LockFileName so projects converge on the canonical layout.
const LegacyLockFileName = "qvr.lock.json"

// MinSupportedLockFileVersion is the oldest schema this binary will read.
// Anything older is rejected at ReadLockFile time so a corrupted or
// hand-edited file doesn't load as an empty lockfile.
const MinSupportedLockFileVersion = 2

var (
	ErrLockNotFound          = errors.New("lock file not found")
	ErrLockSkillMissing      = errors.New("skill not present in lock file")
	ErrLockVersionUnsupported = errors.New("unsupported lock file version")
)

// LockEntry records a single installed skill's filesystem and git state.
type LockEntry struct {
	Name        string    `json:"name"`
	Registry    string    `json:"registry"`
	Path        string    `json:"path"` // relative path inside the registry repo
	Branch      string    `json:"branch"`
	Commit      string    `json:"commit"`
	Worktree    string    `json:"worktree"`
	Targets     []string  `json:"targets"`
	Global      bool      `json:"global"`
	InstalledAt time.Time `json:"installedAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	// Source is "registry", "subdir", "standalone", or "link". Defaults to
	// "registry". "subdir" means the entry was created by `qvr add <url>`
	// against a sparse checkout of one folder inside a multi-skill repo
	// (no entry in config.Registries — the upstream URL lives on this
	// entry as RepoURL instead).
	Source string `json:"source,omitempty"`
	// RepoURL is the canonical clone URL the skill was installed from.
	// Required for Source == "subdir" so commands like `qvr outdated`
	// can ls-remote without a matching registry config. Optional for
	// Source == "registry" (the registry config still owns the URL).
	RepoURL string `json:"repoURL,omitempty"`
	// LinkTarget is the absolute path of the source dir when Source == "link".
	LinkTarget string `json:"linkTarget,omitempty"`
	// Disabled hides the skill from agents without removing the worktree.
	// `qvr disable` flips this to true and tears down symlinks; `qvr enable`
	// reverses both.
	Disabled bool `json:"disabled,omitempty"`
	// Verification carries the supply-chain provenance block. nil for
	// entries written by pre-v3 binaries (and for fresh v3 installs that
	// haven't yet been hashed). Populated post-install by the installer
	// and refreshed by `qvr lock verify` / `qvr lock upgrade`.
	Verification *VerificationRecord `json:"verification,omitempty"`
}

// LockFile is the on-disk record of installed skills.
type LockFile struct {
	Version int                   `json:"version"`
	Skills  map[string]*LockEntry `json:"skills"`
	path    string                // canonical write destination — not serialized
	// legacyPath, if non-empty, is the qvr.lock.json the contents were read
	// from. Write() removes it after a successful write so the project ends
	// up with a single canonical qvr.lock instead of two side-by-side files.
	legacyPath string
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

// ReadLockFile loads the lock file at path. Returns an empty lock file when
// the path does not exist — this is the expected state before the first
// install.
//
// Falls back to <dir>/qvr.lock.json (the pre-v0.4.6 filename) when path does
// not exist, so projects upgrading across the rename don't appear empty. The
// returned LockFile remembers the legacy path; the next Write() lands at
// path and removes the legacy file so the project converges on one filename.
//
// Rejects on-disk files whose `version` field is missing/zero, below
// MinSupportedLockFileVersion, or above LockFileVersion (a future binary
// wrote it). The error wraps ErrLockVersionUnsupported so callers can
// distinguish "broken file" from "missing file".
func ReadLockFile(path string) (*LockFile, error) {
	l := NewLockFile(path)
	data, err := os.ReadFile(path)
	legacyPath := ""
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read lock file: %w", err)
		}
		// Canonical filename absent — try the legacy filename next to it.
		// Only used for projects mid-upgrade from <v0.4.6.
		legacyCandidate := filepath.Join(filepath.Dir(path), LegacyLockFileName)
		if legacyCandidate != path {
			legacyData, legacyErr := os.ReadFile(legacyCandidate)
			if legacyErr == nil {
				data = legacyData
				legacyPath = legacyCandidate
			} else if !os.IsNotExist(legacyErr) {
				return nil, fmt.Errorf("read legacy lock file: %w", legacyErr)
			}
		}
	}
	if len(data) == 0 {
		return l, nil
	}
	// Zero out Version before Unmarshal — NewLockFile seeds it to
	// LockFileVersion, but json.Unmarshal won't reset fields that aren't
	// in the input, so we'd silently accept a missing `version` key.
	l.Version = 0
	if err := json.Unmarshal(data, l); err != nil {
		return nil, fmt.Errorf("parse lock file: %w", err)
	}
	if l.Version == 0 {
		return nil, fmt.Errorf("%w: lock file missing `version` field — run `qvr lock upgrade` against a known-good source",
			ErrLockVersionUnsupported)
	}
	if l.Version < MinSupportedLockFileVersion {
		return nil, fmt.Errorf("%w: version %d is older than the minimum supported (%d) — this binary cannot read it",
			ErrLockVersionUnsupported, l.Version, MinSupportedLockFileVersion)
	}
	if l.Version > LockFileVersion {
		return nil, fmt.Errorf("%w: version %d was written by a newer qvr (this binary writes v%d) — upgrade qvr",
			ErrLockVersionUnsupported, l.Version, LockFileVersion)
	}
	l.path = path
	l.legacyPath = legacyPath
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
	// Best-effort cleanup if we fail mid-write.
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
	// Best-effort cleanup of the legacy filename after a successful write.
	// Failure here is non-fatal — the canonical file is already on disk, and
	// `qvr doctor` will surface a leftover qvr.lock.json on its next run.
	if l.legacyPath != "" && l.legacyPath != l.path {
		_ = os.Remove(l.legacyPath)
		l.legacyPath = ""
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

// DefaultLockPath returns the lock path based on whether --global was requested.
// Local installs write alongside the project; global installs write under the
// quiver home directory (caller supplies it).
func DefaultLockPath(projectRoot, quiverHome string, global bool) string {
	if global {
		return filepath.Join(quiverHome, LockFileName)
	}
	return filepath.Join(projectRoot, LockFileName)
}
