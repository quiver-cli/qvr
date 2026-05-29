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
// v5 slims the v4 shape. Drops dead fields (worktree, installPath,
// updatedAt, repoURL, linkTarget), drops duplicate fields (the verification
// block's provenance sub-object, treeSHA, commitSHA), and hoists the
// load-bearing integrity field (subtreeHash) to the top level. Repurposes
// `source` from an install-method enum to the actual fetch URL (or absolute
// local path for link installs) so the lock is self-contained across
// machines. Renames resolvedSha → commit. The verification block becomes
// omit-when-empty and only surfaces real signals (scan, signature, eval,
// attestation, skill card). qvr is still pre-release; v4 locks are rejected
// outright with a "delete and run qvr sync" message.
const LockFileVersion = 5

// LockFileName is the canonical lock file name.
const LockFileName = "qvr.lock"

// MinSupportedLockFileVersion is the oldest schema this binary will read.
// v5 is a hard break — v4 and older are rejected outright.
const MinSupportedLockFileVersion = 5

var (
	ErrLockNotFound           = errors.New("lock file not found")
	ErrLockSkillMissing       = errors.New("skill not present in lock file")
	ErrLockVersionUnsupported = errors.New("unsupported lock file version")
)

// LockEntry records a single installed skill's filesystem and git state.
//
// The on-disk shape carries only what the lock can't recover from another
// source. Worktree paths are computed via registry.WorktreePath(...) at use
// time, never written (the v4 absolute-path leak made the lock unsafe to
// check in). The skill name is the LockFile.Skills map key, never duplicated
// on the entry. Drift detection compares SubtreeHash to a recomputation
// from disk — that's the integrity check; everything else is metadata.
type LockEntry struct {
	// Name is the in-memory map key (populated by ReadLockFile from the
	// Skills map). Never serialised — `json:"-"` — the map key on disk is
	// authoritative.
	Name string `json:"-"`

	// Registry is the stable display name of the registry this skill came
	// from (e.g. "raks"). Optional: ad-hoc URL installs and link installs
	// have no named registry.
	Registry string `json:"registry,omitempty"`

	// Source is the actual fetch coordinate — a git URL for remote installs,
	// or an absolute local path for link installs. Always present. Makes the
	// lock self-contained: a fresh clone can resolve every skill without
	// needing the original registry name pre-configured in user config.
	Source string `json:"source"`

	// Path is the subpath inside the source repo. Empty for link installs
	// (the whole target tree is the skill).
	Path string `json:"path,omitempty"`

	// Ref is the human label requested at install time (branch, tag, or
	// "local" for link installs). The version identifier.
	Ref string `json:"ref"`

	// Commit is the current git SHA at the entry's HEAD. Pull/Switch
	// advance this as the worktree moves between commits. Empty for link
	// installs.
	Commit string `json:"commit,omitempty"`

	// InstallCommit is the SHA that keyed the on-disk worktree directory
	// at install time. The worktree path is computed as
	// WorktreePath(Registry, Name, ShortSHA(InstallCommit)) so that
	// Pull/Switch advancing Commit does not move the directory out from
	// under the symlinks. Empty for link installs and for entries
	// pre-dating this field, in which case EntryWorktreePath falls back
	// to Commit.
	InstallCommit string `json:"installCommit,omitempty"`

	// SubtreeHash is the canonical content hash of the installed skill
	// subtree. Load-bearing — drift detection compares this to a fresh
	// recomputation from disk.
	SubtreeHash string `json:"subtreeHash"`

	// Targets is the list of agent dirs the skill is symlinked into.
	Targets []string `json:"targets"`

	// InstalledAt is the original install timestamp. Stable across resyncs
	// so the lock diff stays quiet.
	InstalledAt time.Time `json:"installedAt"`

	// Disabled hides the skill from agents without removing the worktree.
	Disabled bool `json:"disabled,omitempty"`

	// Verification carries supply-chain signals: scan results, signatures,
	// attestations, evals. Omitted entirely when there's nothing to record.
	Verification *VerificationRecord `json:"verification,omitempty"`
}

// IsLink reports whether this entry is a local-link install. The link
// installer sets Ref="local" as the canonical marker; no remote install
// uses that ref name (registry refs are branches, tags, or commits). This
// avoids false positives in tests that legitimately use local bare-repo
// paths as registry URLs — Source alone isn't a reliable discriminator.
func (e *LockEntry) IsLink() bool {
	if e == nil {
		return false
	}
	return e.Ref == "local"
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
// Rejects any schema version other than v5 with an error wrapping
// ErrLockVersionUnsupported. qvr is pre-release with no external users, so
// older shapes (v2, v3, v4) are not supported — delete the old lock and
// run `qvr sync` to regenerate.
//
// Populates each entry's in-memory Name field (`json:"-"`) from the map key
// so consumers can rely on entry.Name without re-walking the map.
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
		return nil, fmt.Errorf("%w: qvr.lock is at schema v%d; v%d introduced a slimmer shape — delete qvr.lock and run `qvr sync` to regenerate",
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
	// Populate each entry's in-memory Name from the map key so consumers can
	// rely on entry.Name. The name is `json:"-"` on disk — the key is
	// authoritative.
	for name, entry := range l.Skills {
		if entry != nil {
			entry.Name = name
		}
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

// Put upserts an entry under its Name. The Name field is the in-memory
// shadow of the map key (`json:"-"`), so callers set entry.Name before
// calling. InstalledAt is auto-stamped on first insert and preserved on
// updates.
func (l *LockFile) Put(entry *LockEntry) {
	if l.Skills == nil {
		l.Skills = make(map[string]*LockEntry)
	}
	if entry.InstalledAt.IsZero() {
		entry.InstalledAt = time.Now().UTC()
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
