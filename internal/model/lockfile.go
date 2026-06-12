// Package model holds qvr's core domain types — Skill, Registry, LockFile,
// Target, and the qvr.toml Project file — plus their (de)serialization and
// invariants. It is the shared vocabulary every other internal package
// builds on and depends only on the standard library and codec packages.
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
// v6 makes each skill a single cohesive entry, uv.lock-style. Skills
// serialize as an array of tables (`[[skill]]` with an explicit `name`
// field) instead of a map keyed by name, and the v5 verification wrapper is
// gone: `scan` and `provenance` ride as inline tables directly on the entry.
// Provenance now classifies everything about content origin — commitAuthor,
// the signature check, and the sourceUpstream/forkedFrom lineage markers
// that were top-level v5 fields. Redundancy cuts: the constant
// provenance.provider ("git") is dropped, the writer-less reserved slots
// (eval, signature, attestation, skillCard) are dropped, and installCommit
// is omitted on disk when it equals commit (the read path restores it).
// qvr is still pre-release; v5 locks are rejected outright with a "delete
// and run qvr sync" message.
const LockFileVersion = 6

// LockFileName is the canonical lock file name.
const LockFileName = "qvr.lock"

// MinSupportedLockFileVersion is the oldest schema this binary will read.
// v6 is a hard break — v5 and older are rejected outright.
const MinSupportedLockFileVersion = 6

var (
	// ErrLockNotFound indicates a qvr.lock that was required to exist does
	// not. ReadLockFile itself treats a missing file as an empty lock; this
	// sentinel is for callers that need the absence to be an error.
	ErrLockNotFound = errors.New("lock file not found")
	// ErrLockSkillMissing is returned (wrapped, with the skill name) by
	// LockFile.Get and Remove when the named skill has no entry.
	ErrLockSkillMissing = errors.New("skill not present in lock file")
	// ErrLockVersionUnsupported is returned (wrapped, with detail) when a
	// lock's version field is missing, malformed, or outside the
	// [MinSupportedLockFileVersion, LockFileVersion] window this binary reads.
	ErrLockVersionUnsupported = errors.New("unsupported lock file version")
)

// Install mode constants. Empty string means "shared" (default add semantics)
// for backward compatibility — older v5 locks predate this field.
const (
	ModeShared = ""      // symlink → ~/.quiver/worktrees/.../  (default for `qvr add`)
	ModeEdit   = "edit"  // canonical real dir at EditPath (set by `qvr edit`)
	ModeLink   = "link"  // absolute path in Source (legacy `qvr link`; read-only compat)
	ModeLocal  = "local" // immutable copy of a local folder (set by `qvr add --local`)
)

// LocalRegistry is the reserved registry name stamped on `qvr add --local`
// entries. It is not a configured registry — it only namespaces the worktree
// copy under ~/.quiver/worktrees/_local/. The leading underscore can never
// collide with a user-registered registry: ValidateRegistryName requires each
// segment to start with [a-z0-9].
const LocalRegistry = "_local"

// LockEntry records a single installed skill's filesystem and git state.
//
// The on-disk shape carries only what the lock can't recover from another
// source. Worktree paths are computed via registry.WorktreePath(...) at use
// time, never written (the v4 absolute-path leak made the lock unsafe to
// check in). Each entry is one `[[skill]]` element identified by its Name
// field, uv.lock-style. Drift detection compares SubtreeHash to a
// recomputation from disk — that's the integrity check; everything else is
// metadata.
type LockEntry struct {
	// Name is the skill's lock identity — unique across the file,
	// enforced on read. In memory it doubles as the Skills map key.
	Name string `json:"name" toml:"name"`

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
	// under the symlinks. Usually equal to Commit — the redundant copy is
	// omitted on disk and restored on read; it only diverges (and
	// serializes) after Pull/Switch. Empty for link installs.
	InstallCommit string `json:"installCommit,omitempty" toml:"installCommit,omitempty"`

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

	// Scan summarises the scan-gate pass over this entry's content.
	// Omitted when no scan has been recorded.
	Scan *ScanRef `json:"scan,omitempty" toml:"scan,omitempty,inline"`

	// Provenance classifies content origin: commit author, git-native
	// signature check, and source-lineage markers (upstream, forkedFrom).
	// Omitted when there's nothing to record.
	Provenance *ProvenanceRef `json:"provenance,omitempty" toml:"provenance,omitempty,inline"`
}

// AuthorIdentity returns the recorded commit author (`Name <email>`) or ""
// when no provenance was captured. Nil-safe accessor for the v6 location of
// the v5 top-level commitAuthor field.
func (e *LockEntry) AuthorIdentity() string {
	if e == nil || e.Provenance == nil {
		return ""
	}
	return e.Provenance.CommitAuthor
}

// SignatureStatus returns the recorded git signature status (verified |
// none | invalid) or "" when the signature was never checked.
func (e *LockEntry) SignatureStatus() string {
	if e == nil || e.Provenance == nil {
		return ""
	}
	return e.Provenance.SignatureStatus
}

// UpstreamSource returns the original upstream URL when the entry has moved
// off its first source (provenance.upstream, written by `qvr edit` /
// `qvr publish --fork --migrate`), falling back to Source.
func (e *LockEntry) UpstreamSource() string {
	if e == nil {
		return ""
	}
	if e.Provenance != nil && e.Provenance.Upstream != "" {
		return e.Provenance.Upstream
	}
	return e.Source
}

// EnsureProvenance returns the entry's provenance block, allocating it
// first when nil. Use when writing a single provenance field; pair with
// NormalizeProvenance so an all-empty block doesn't serialize.
func (e *LockEntry) EnsureProvenance() *ProvenanceRef {
	if e.Provenance == nil {
		e.Provenance = &ProvenanceRef{}
	}
	return e.Provenance
}

// NormalizeProvenance drops an all-empty provenance block so it stays
// omitted from disk.
func (e *LockEntry) NormalizeProvenance() {
	if e.Provenance.IsEmpty() {
		e.Provenance = nil
	}
}

// IsLink reports whether this entry is a legacy local-link install (a live
// symlink straight at an external path, set by the now-removed `qvr link`).
// The link installer set Ref="local" as the canonical marker; no remote
// install uses that ref name (registry refs are branches, tags, or commits).
//
// The `Mode == ""` qualifier on the Ref="local" heuristic is load-bearing:
// `qvr add --local` entries (ModeLocal) also carry Ref="local" but are
// immutable copies, not live links — they must NOT be treated as link
// installs. Explicit ModeLink entries still match for forward reads of any
// pre-existing lock written by the old `qvr link`.
func (e *LockEntry) IsLink() bool {
	if e == nil {
		return false
	}
	return e.Mode == ModeLink || (e.Ref == "local" && e.Mode == ModeShared)
}

// IsLocal reports whether this entry is an immutable local copy installed via
// `qvr add --local`. The materialized content lives in a hash-keyed worktree
// under the reserved LocalRegistry namespace; there is no git upstream, so
// registry-scoped operations (update, outdated, sync git ops) skip it.
func (e *LockEntry) IsLocal() bool {
	if e == nil {
		return false
	}
	return e.Mode == ModeLocal
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
//
// The lockfile is qvr's pillar of portability, governance, and reproducibility:
// committed to the repo, it is the resolved source of truth a fresh clone
// replays to reconstruct a byte-identical skill set. It is self-sufficient —
// `qvr sync` reproduces from the lock alone. Declarative project intent (the
// skills a project wants, its default agent targets) lives in qvr.toml; the
// lock records the resolved state those declarations pin to.
type LockFile struct {
	Version int `json:"version" toml:"version"`

	// Skills is the in-memory index, keyed by entry name. On disk (v6) it
	// serializes as a `[[skill]]` array of tables sorted by name — each
	// skill one cohesive uv.lock-style entry — via lockFileDisk.
	Skills map[string]*LockEntry `json:"skills" toml:"-"`
	path   string                // canonical write destination — not serialized
}

// lockFileDisk is the v6 on-disk TOML shape: a [[skill]] array of tables,
// each element one skill package with an explicit `name` field, like
// uv.lock's [[package]].
type lockFileDisk struct {
	Version int          `toml:"version"`
	Skills  []*LockEntry `toml:"skill,omitempty"`
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
// Rejects any schema version other than v6 with an error wrapping
// ErrLockVersionUnsupported. qvr is pre-release with no external users, so
// older shapes (v2–v5) are not supported — delete the old lock and run
// `qvr sync` to regenerate.
//
// Builds the in-memory Skills map from the on-disk `[[skill]]` array,
// rejecting duplicate names, and restores InstallCommit (omitted on disk
// when it equals Commit) so in-memory consumers see the install-time
// invariant.
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
	// Validate the version BEFORE the strict shape unmarshal: legacy locks
	// (v5 and older keyed skills as `[skills.<name>]` tables) can't parse
	// into the v6 array shape, and the user should see the friendly
	// "delete and resync" message rather than a raw TOML type error.
	version, err := lockVersionOf(data)
	if err != nil {
		return nil, err
	}
	var disk lockFileDisk
	if err := toml.Unmarshal(data, &disk); err != nil {
		return nil, fmt.Errorf("parse lock file: %w", err)
	}
	l.Version = version
	l.path = path
	if err := l.indexEntries(disk.Skills); err != nil {
		return nil, err
	}
	return l, nil
}

// lockVersionOf probes the raw TOML for a supported integer `version` field,
// returning the friendly ErrLockVersionUnsupported variants for missing,
// mistyped, legacy, and too-new values.
func lockVersionOf(data []byte) (int, error) {
	var probe struct {
		Version int `toml:"version"`
	}
	if err := toml.Unmarshal(data, &probe); err != nil {
		if rawVersion := extractRawVersion(data); rawVersion != "" {
			return 0, fmt.Errorf("%w: `version` must be an integer TOML value, got %s — delete the lock and reinstall",
				ErrLockVersionUnsupported, rawVersion)
		}
		return 0, fmt.Errorf("parse lock file: %w", err)
	}
	switch {
	case probe.Version == 0:
		return 0, fmt.Errorf("%w: lock file missing `version` field — delete the lock and reinstall",
			ErrLockVersionUnsupported)
	case probe.Version < MinSupportedLockFileVersion:
		return 0, fmt.Errorf("%w: qvr.lock is at schema v%d; v%d made each skill a single [[skill]] entry — delete qvr.lock and run `qvr sync` to regenerate",
			ErrLockVersionUnsupported, probe.Version, MinSupportedLockFileVersion)
	case probe.Version > LockFileVersion:
		return 0, fmt.Errorf("%w: version %d was written by a newer qvr (this binary writes v%d) — upgrade qvr",
			ErrLockVersionUnsupported, probe.Version, LockFileVersion)
	}
	return probe.Version, nil
}

// indexEntries builds the in-memory Skills map from the on-disk [[skill]]
// array, rejecting nameless or duplicate entries and restoring the
// InstallCommit invariant (omitted on disk when equal to Commit).
func (l *LockFile) indexEntries(entries []*LockEntry) error {
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		if entry.Name == "" {
			return fmt.Errorf("parse lock file: [[skill]] entry missing `name`")
		}
		if _, dup := l.Skills[entry.Name]; dup {
			return fmt.Errorf("parse lock file: duplicate [[skill]] entry %q", entry.Name)
		}
		if entry.InstallCommit == "" {
			entry.InstallCommit = entry.Commit
		}
		l.Skills[entry.Name] = entry
	}
	return nil
}

// MarshalLockFile serializes a lock file using the canonical on-disk TOML
// format. Callers that need to compare planned writes with prior bytes should
// use this helper so idempotency checks match Write exactly.
func MarshalLockFile(l *LockFile) ([]byte, error) {
	l.Version = LockFileVersion
	if l.Skills == nil {
		l.Skills = make(map[string]*LockEntry)
	}

	disk := lockFileDisk{Version: LockFileVersion}
	for _, e := range l.Entries() {
		// Shallow-copy so on-disk redundancy trimming never mutates the
		// caller's live entry.
		ec := *e
		if ec.InstallCommit == ec.Commit {
			ec.InstallCommit = ""
		}
		if ec.Provenance.IsEmpty() {
			ec.Provenance = nil
		}
		disk.Skills = append(disk.Skills, &ec)
	}

	data, err := toml.Marshal(disk)
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
