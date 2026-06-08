package model

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/pelletier/go-toml/v2"
)

// ProjectFileName is the canonical human-authored project config file name.
const ProjectFileName = "qvr.toml"

var (
	// ErrProjectNotFound is returned when no qvr.toml exists where one was expected.
	ErrProjectNotFound = errors.New("project file not found")
	// ErrProjectSkillMissing is returned when a skill coordinate is absent from qvr.toml.
	ErrProjectSkillMissing = errors.New("skill not present in project file")
)

// ProjectFile is the declarative, human-authored front door (qvr.toml).
//
// It is qvr's intent layer: a teammate can hand-edit it, commit it, and run
// `qvr sync` to materialise the declared set. It is ADDITIVE — qvr.lock remains
// the self-sufficient resolved state, and `qvr sync` reproduces from the lock
// ALONE (CI never needs qvr.toml). The two files are kept consistent by
// write-through on every mutating command; divergence (from hand-edits or git
// merges) is reconciled by `qvr sync` (lock wins) or `qvr lock --from-toml`
// (toml wins).
//
// Only [project] and [skills] are live today. Plugins/Hooks/Mcp are reserved
// sections — round-tripped losslessly via map[string]any so a user pre-authoring
// them survives a qvr-managed rewrite, and so future milestones (plugins → hooks
// → mcp) are purely additive.
type ProjectFile struct {
	Project ProjectMeta `toml:"project" json:"project"`

	// Skills maps an install coordinate ("<registry>/<skill>", e.g.
	// "anthropics/skills/frontend-design") to a requested ref (branch or tag).
	// Registry-sourced skills only — edit/link/local install modes have no
	// coordinate and live solely in qvr.lock.
	Skills map[string]string `toml:"skills,omitempty" json:"skills,omitempty"`

	// Plugins/Hooks/Mcp are reserved for future milestones. Inert this release;
	// map[string]any keeps any hand-authored content lossless on round-trip.
	Plugins map[string]any `toml:"plugins,omitempty" json:"plugins,omitempty"`
	Hooks   map[string]any `toml:"hooks,omitempty" json:"hooks,omitempty"`
	Mcp     map[string]any `toml:"mcp,omitempty" json:"mcp,omitempty"`

	path string // canonical write destination — not serialized
}

// ProjectMeta is the [project] table.
type ProjectMeta struct {
	// Name is the project identifier (optional; qvr init defaults to the
	// directory name). Inert for a pure consumer project, reserved so a
	// project can later be published as a bundle without a schema change.
	Name string `toml:"name,omitempty" json:"name,omitempty"`
	// Version is the project version (optional; qvr init defaults to "0.1.0").
	Version string `toml:"version,omitempty" json:"version,omitempty"`

	// DefaultTargets is the project's agent routing policy: the targets a bare
	// `qvr add <skill>` installs into when no --target flag is passed. Set by
	// `qvr target add`. Stored here (not in machine-local config) so it travels
	// with the repo. Canonical target names only (aliases normalised on write).
	// Empty means "fall back to config default_target".
	DefaultTargets []string `toml:"default-targets,omitempty" json:"defaultTargets,omitempty"`
}

// NewProjectFile returns an empty project file at the given path.
func NewProjectFile(path string) *ProjectFile {
	return &ProjectFile{
		Skills: make(map[string]string),
		path:   path,
	}
}

// Path returns the project file's on-disk path.
func (p *ProjectFile) Path() string { return p.path }

// SetPath overrides the project file's on-disk path.
func (p *ProjectFile) SetPath(path string) { p.path = path }

// DefaultProjectPath returns the qvr.toml path for a project root. qvr.toml is
// project-only — there is no global variant (a global qvr.toml has no coherent
// meaning; global installs are lock-only).
func DefaultProjectPath(projectRoot string) string {
	return filepath.Join(projectRoot, ProjectFileName)
}

// ReadProjectFile loads the project file at path. Returns an empty project file
// when the path does not exist or is empty — that is the normal state for a
// project that has not adopted qvr.toml yet, and is what preserves the lock's
// self-sufficiency (an absent qvr.toml is a no-op everywhere).
func ReadProjectFile(path string) (*ProjectFile, error) {
	p := NewProjectFile(path)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return p, nil
		}
		return nil, fmt.Errorf("read project file: %w", err)
	}
	if len(data) == 0 {
		return p, nil
	}
	if err := toml.Unmarshal(data, p); err != nil {
		return nil, fmt.Errorf("parse project file: %w", err)
	}
	p.path = path
	if p.Skills == nil {
		p.Skills = make(map[string]string)
	}
	return p, nil
}

// MarshalProjectFile serializes a project file using the canonical on-disk TOML
// format. Callers comparing a planned write to prior bytes (idempotency) should
// use this so the comparison matches Write exactly.
func MarshalProjectFile(p *ProjectFile) ([]byte, error) {
	data, err := toml.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("marshal project file: %w", err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	return data, nil
}

// Write persists the project file atomically: write to a temp sibling, then
// rename. Mirrors LockFile.Write.
func (p *ProjectFile) Write() error {
	if p.path == "" {
		return errors.New("project file path not set")
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0o755); err != nil {
		return fmt.Errorf("create project dir: %w", err)
	}

	data, err := MarshalProjectFile(p)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(p.path), ".qvr-toml-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp project file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp project file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp project file: %w", err)
	}
	if err := os.Rename(tmpPath, p.path); err != nil {
		cleanup()
		return fmt.Errorf("rename temp project file: %w", err)
	}
	return nil
}

// PutSkill upserts a skill coordinate → ref entry.
func (p *ProjectFile) PutSkill(coordinate, ref string) {
	if p.Skills == nil {
		p.Skills = make(map[string]string)
	}
	p.Skills[coordinate] = ref
}

// GetSkill returns the ref for a coordinate, or ErrProjectSkillMissing.
func (p *ProjectFile) GetSkill(coordinate string) (string, error) {
	if ref, ok := p.Skills[coordinate]; ok {
		return ref, nil
	}
	return "", fmt.Errorf("%w: %s", ErrProjectSkillMissing, coordinate)
}

// RemoveSkill deletes a skill coordinate. A no-op (nil) if absent — removal
// must be idempotent so write-through on `qvr remove` never errors when the
// entry was non-portable (edit/link/local) and never had a coordinate.
func (p *ProjectFile) RemoveSkill(coordinate string) {
	delete(p.Skills, coordinate)
}

// Skills entries are sorted by coordinate for stable iteration.
func (p *ProjectFile) SkillCoordinates() []string {
	out := make([]string, 0, len(p.Skills))
	for c := range p.Skills {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// Validate checks structural invariants: every coordinate and ref is non-empty.
func (p *ProjectFile) Validate() error {
	for coord, ref := range p.Skills {
		if coord == "" {
			return errors.New("project file has a skill entry with an empty coordinate")
		}
		if ref == "" {
			return fmt.Errorf("project file skill %q has an empty ref", coord)
		}
	}
	return nil
}

// AddDefaultTargets unions names into the project's default target set, keeping
// the list sorted and deduplicated. Returns the names that were newly added so
// callers can report precisely what changed. Names are assumed canonical — the
// command layer normalises aliases before calling.
func (p *ProjectFile) AddDefaultTargets(names ...string) []string {
	existing := make(map[string]struct{}, len(p.Project.DefaultTargets))
	for _, n := range p.Project.DefaultTargets {
		existing[n] = struct{}{}
	}
	var added []string
	for _, n := range names {
		if _, ok := existing[n]; ok {
			continue
		}
		existing[n] = struct{}{}
		added = append(added, n)
	}
	if len(added) == 0 {
		return nil
	}
	p.Project.DefaultTargets = append(p.Project.DefaultTargets, added...)
	sort.Strings(p.Project.DefaultTargets)
	return added
}

// RemoveDefaultTargets drops names from the project's default target set,
// returning the names that were actually present and removed.
func (p *ProjectFile) RemoveDefaultTargets(names ...string) []string {
	if len(p.Project.DefaultTargets) == 0 {
		return nil
	}
	drop := make(map[string]struct{}, len(names))
	for _, n := range names {
		drop[n] = struct{}{}
	}
	kept := p.Project.DefaultTargets[:0:0]
	var removed []string
	for _, n := range p.Project.DefaultTargets {
		if _, ok := drop[n]; ok {
			removed = append(removed, n)
			continue
		}
		kept = append(kept, n)
	}
	p.Project.DefaultTargets = kept
	return removed
}

// SkillCoordinate returns the qvr.toml [skills] key for a lock entry, or "" if
// the entry is not representable in qvr.toml. The coordinate is
// "<registry>/<localName>" — registry-sourced shared installs only.
//
// Returns "" (entry is lock-only) when:
//   - the install mode is not shared (edit/link/local have no fetch coordinate), or
//   - there is no named registry (ad-hoc URL installs).
//
// The local name (entry.Name, the lock map key) is used rather than the
// canonical registry name so two `--as` installs of the same upstream skill get
// distinct, collision-free toml keys. For the common (non-aliased) case
// entry.Name == the canonical name, so the coordinate is also a valid fetch
// spec. Aliased installs are the one place the toml key is lossy versus the
// lock — the lock stays authoritative for reproduction.
func SkillCoordinate(e *LockEntry) string {
	if e == nil {
		return ""
	}
	if e.Mode != ModeShared {
		return ""
	}
	if e.Registry == "" {
		return ""
	}
	name := e.Name
	if name == "" {
		name = e.Canonical
	}
	if name == "" {
		return ""
	}
	return e.Registry + "/" + name
}
