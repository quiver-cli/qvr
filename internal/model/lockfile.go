package model

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// RootSkillContentDirs are the directories (alongside SKILL.md) that make up a
// root-layout skill's installable/scannable content when it shares a repo with
// sibling skills. App code (bin/, lib/, test/, …) is deliberately excluded.
var RootSkillContentDirs = []string{"references", "scripts", "assets"}

// SkillScopePaths returns the repo-relative paths that make up a skill's
// installable and scannable content, given its source subpath and whether it is
// a root skill coexisting with siblings. It is the single source of truth shared
// by the index, the scan gate, and the installer.
//
//   - A non-root skill owns its whole subtree → [path].
//   - A lone root skill owns the whole repo → nil ("no sparse narrowing").
//   - A root skill coexisting with siblings owns only SKILL.md + the recognized
//     content dirs — never the siblings or unrelated app code.
func SkillScopePaths(path string, rootCoexists bool) []string {
	if path != "" && path != "." {
		return []string{path}
	}
	if rootCoexists {
		return append([]string{"SKILL.md"}, RootSkillContentDirs...)
	}
	return nil
}

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

// Install mode constants. Empty string means "shared" (default add semantics)
// for backward compatibility — older v5 locks predate this field.
const (
	ModeShared = ""     // symlink → ~/.quiver/worktrees/.../  (default for `qvr add`)
	ModeEdit   = "edit" // canonical real dir at EditPath (set by `qvr edit`)
	ModeLink   = "link" // absolute path in Source (set by `qvr link`)
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
	// Skills map). Never serialised — the map key on disk is
	// authoritative.
	Name string `json:"-" toml:"-"`

	// Registry is the stable display name of the registry this skill came
	// from (e.g. "raks"). Optional: ad-hoc URL installs and link installs
	// have no named registry.
	Registry string `json:"registry,omitempty" toml:"registry,omitempty"`

	// Source is the actual fetch coordinate — a git URL for remote installs,
	// or an absolute local path for link installs. Always present. Makes the
	// lock self-contained: a fresh clone can resolve every skill without
	// needing the original registry name pre-configured in user config.
	Source string `json:"source" toml:"source"`

	// Path is the subpath inside the source repo. Empty for link installs
	// (the whole target tree is the skill).
	Path string `json:"path,omitempty" toml:"path,omitempty"`

	// RootCoexists records that this is a root-layout skill (Path ".") that
	// shared its repo with sibling skill directories at install time, so its
	// content was scoped to SKILL.md + the recognized content dirs rather than
	// the whole repo. Persisted so a reproducible restore (`qvr sync` at the
	// pinned commit) re-applies the same scope even if the registry's current
	// HEAD no longer reports the sibling layout. See SkillScopePaths.
	RootCoexists bool `json:"rootCoexists,omitempty" toml:"rootCoexists,omitempty"`

	// Canonical is the registry-side skill name when the user installed
	// under an alias via `qvr add <skill> --as <alias>`. Empty when the
	// lock key already matches the canonical name (the common case).
	// Lookups against the upstream index (e.g. `qvr upgrade` checking
	// for new tags) consult Canonical when set so the alias still resolves
	// back to the skill it points at.
	Canonical string `json:"canonical,omitempty" toml:"canonical,omitempty"`

	// Ref is the human label requested at install time (branch, tag, or
	// "local" for link installs). The version identifier.
	Ref string `json:"ref" toml:"ref"`

	// Commit is the current git SHA at the entry's HEAD. Pull/Switch
	// advance this as the worktree moves between commits. Empty for link
	// installs.
	Commit string `json:"commit,omitempty" toml:"commit,omitempty"`

	// InstallCommit is the SHA that keyed the on-disk worktree directory
	// at install time. The worktree path is computed as
	// WorktreePath(Registry, Name, ShortSHA(InstallCommit)) so that
	// Pull/Switch advancing Commit does not move the directory out from
	// under the symlinks. Empty for link installs and for entries
	// pre-dating this field, in which case EntryWorktreePath falls back
	// to Commit.
	InstallCommit string `json:"installCommit,omitempty" toml:"installCommit,omitempty"`

	// CommitAuthor is the author identity on the installed commit, formatted as
	// `Name <email>`. Trust policy can pin allowed authors per registry.
	CommitAuthor string `json:"commitAuthor,omitempty" toml:"commitAuthor,omitempty"`

	// SubtreeHash is the canonical content hash of the installed skill
	// subtree. Load-bearing — drift detection compares this to a fresh
	// recomputation from disk.
	SubtreeHash string `json:"subtreeHash" toml:"subtreeHash"`

	// TreeOID is the native git tree object ID of the installed subtree
	// (e.g. `git rev-parse <commit>:<path>`). Informational only — the
	// load-bearing integrity anchor is SubtreeHash, which normalises line
	// endings and works for non-git editable installs. TreeOID is recorded
	// for uv-style git-native identity and future content dedup. Empty for
	// link installs and entries whose hash computation failed.
	TreeOID string `json:"treeOID,omitempty" toml:"treeOID,omitempty"`

	// SourceUpstream records the original upstream URL when an entry has
	// moved off its first source — set by `qvr edit` (mirrors Source at
	// eject time) and preserved through `qvr publish --fork --migrate`
	// (when Source flips to the fork URL). Empty for entries that never
	// diverged. Provenance only — never used to drive pushes.
	SourceUpstream string `json:"sourceUpstream,omitempty" toml:"sourceUpstream,omitempty"`

	// ForkedFrom records the upstream this skill was forked from when
	// published via `qvr publish --fork --migrate`. Format:
	// "<git-url>@<commit-sha>" (short sha). Empty for skills never
	// forked. Set on the author's local lock at migrate time; the
	// published artifact's SKILL.md is never mutated, so downstream
	// consumers don't carry this field unless they themselves migrate.
	// Provenance only in v0.8; v0.9's trust layer will read this to
	// verify fork policy.
	ForkedFrom string `json:"forkedFrom,omitempty" toml:"forkedFrom,omitempty"`

	// Mode is the install mode: "" (shared, default for `qvr add`),
	// "edit" (`qvr edit` ejected to EditPath), or "link" (`qvr link`).
	// Empty-string default keeps existing v5 locks loading unchanged.
	Mode string `json:"mode,omitempty" toml:"mode,omitempty"`

	// EditPath is the project-relative path of the canonical edit copy
	// when Mode == "edit" (e.g. ".claude/skills/auth"). All target
	// symlinks point at this directory; siblings beyond the canonical
	// target carry relative symlinks to it. Empty when Mode != "edit".
	EditPath string `json:"editPath,omitempty" toml:"editPath,omitempty"`

	// Targets is the list of agent dirs the skill is symlinked into.
	Targets []string `json:"targets" toml:"targets"`

	// InstalledAt is the original install timestamp. Stable across resyncs
	// so the lock diff stays quiet.
	InstalledAt time.Time `json:"installedAt" toml:"installedAt"`

	// Disabled hides the skill from agents without removing the worktree.
	Disabled bool `json:"disabled,omitempty" toml:"disabled,omitempty"`

	// Verification carries supply-chain signals: scan results, signatures,
	// attestations, evals. Omitted entirely when there's nothing to record.
	Verification *VerificationRecord `json:"verification,omitempty" toml:"verification,omitempty"`
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
	return e.Ref == "local" || e.Mode == ModeLink
}

// IsEdit reports whether this entry has been ejected into the project
// via `qvr edit`. The agent target dir at EditPath is the canonical
// source of truth — the shared worktree under ~/.quiver/worktrees/ is
// no longer load-bearing for this skill.
func (e *LockEntry) IsEdit() bool {
	if e == nil {
		return false
	}
	return e.Mode == ModeEdit
}

// LockFile is the on-disk record of installed skills.
type LockFile struct {
	Version int                   `json:"version" toml:"version"`
	Skills  map[string]*LockEntry `json:"skills" toml:"skills"`
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
	// LockFileVersion, but toml.Unmarshal won't reset fields that aren't
	// in the input, so we'd silently accept a missing `version` key.
	l.Version = 0
	if err := toml.Unmarshal(data, l); err != nil {
		if rawVersion := extractRawVersion(data); rawVersion != "" {
			return nil, fmt.Errorf("%w: `version` must be an integer TOML value, got %s — delete the lock and reinstall",
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

// MarshalLockFile serializes a lock file using the canonical on-disk TOML
// format. Callers that need to compare planned writes with prior bytes should
// use this helper so idempotency checks match Write exactly.
func MarshalLockFile(l *LockFile) ([]byte, error) {
	l.Version = LockFileVersion
	if l.Skills == nil {
		l.Skills = make(map[string]*LockEntry)
	}

	data, err := toml.Marshal(l)
	if err != nil {
		return nil, fmt.Errorf("marshal lock file: %w", err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	return data, nil
}

// Write persists the lock file atomically: write to a temp sibling, then rename.
func (l *LockFile) Write() error {
	if l.path == "" {
		return errors.New("lock file path not set")
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return fmt.Errorf("create lock dir: %w", err)
	}

	data, err := MarshalLockFile(l)
	if err != nil {
		return err
	}

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
// updates so a no-op re-add doesn't churn the lockfile diff (issue #77).
func (l *LockFile) Put(entry *LockEntry) {
	if l.Skills == nil {
		l.Skills = make(map[string]*LockEntry)
	}
	if entry.InstalledAt.IsZero() {
		if prior, ok := l.Skills[entry.Name]; ok && !prior.InstalledAt.IsZero() {
			entry.InstalledAt = prior.InstalledAt
		} else {
			entry.InstalledAt = time.Now().UTC()
		}
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

// extractRawVersion pulls the `version` field out of the raw TOML without
// caring about its type, so the friendly error message can echo what the
// user actually wrote (e.g. `"three"`).
func extractRawVersion(data []byte) string {
	var probe map[string]any
	if err := toml.Unmarshal(data, &probe); err != nil {
		return ""
	}
	v, ok := probe["version"]
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strconv.Quote(t)
	case bool:
		return strconv.FormatBool(t)
	case []any:
		return fmt.Sprint(t)
	default:
		return fmt.Sprint(t)
	}
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
