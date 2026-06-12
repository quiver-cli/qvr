package model

import (
	"bytes"
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
// qvr.toml is dual-intent: [project] and [skills] are the consumer side (what
// this repo installs), while [registry] is the producer side (how this repo is
// indexed when added as a registry — see internal/registry). Plugins/Hooks/Mcp
// are reserved sections. [registry] and the reserved sections are round-tripped
// losslessly via map[string]any so hand-authored content survives a qvr-managed
// rewrite, and so future milestones (plugins → hooks → mcp) are purely additive.
type ProjectFile struct {
	Project ProjectMeta `toml:"project" json:"project"`

	// Skills maps an install coordinate ("<registry>/<skill>", e.g.
	// "anthropics/skills/frontend-design") to its declared intent. Each value is
	// polymorphic, decoded by go-toml into one of two shapes:
	//
	//   'reg/skill' = 'main'                              # bare ref — uses default-targets
	//   'reg/skill' = { ref = 'main', targets = ['x'] }  # ref + per-skill target override
	//
	// The bare-string form routes the skill to [project].default-targets; the
	// inline-table form pins a per-skill target set that survives a front-door
	// regenerate (qvr add --target / qvr sync). Hold the map as map[string]any —
	// go-toml decodes a bare value to string and an inline table to
	// map[string]any natively (map values can't drive a custom unmarshaler since
	// they aren't addressable). Read via the Skill*/HasSkill accessors and mutate
	// via PutSkill/PutSkillSpec rather than indexing the raw values.
	//
	// Registry-sourced skills only — edit/link/local install modes have no
	// coordinate and live solely in qvr.lock.
	Skills map[string]any `toml:"skills,omitempty" json:"skills,omitempty"`

	// Registry is the producer half of qvr.toml's dual intent: the registry
	// manifest ([registry] name/skills-dir/ignore) that scopes skill discovery
	// when OTHER people's qvr indexes this repo as a registry. It is consumed
	// by internal/registry from the repo's HEAD blob — the consumer side never
	// interprets it, so it is held as map[string]any purely to round-trip
	// hand-authored content losslessly through qvr-managed rewrites.
	Registry map[string]any `toml:"registry,omitempty" json:"registry,omitempty"`

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
		Skills: make(map[string]any),
		path:   path,
	}
}

// SkillSpec is the parsed intent for one qvr.toml [skills] entry: a requested
// ref plus an optional per-skill target override. An empty Targets means the
// skill routes to [project].default-targets (the bare-string form on disk).
type SkillSpec struct {
	Ref     string   `json:"ref"`
	Targets []string `json:"targets,omitempty"`
}

// parseSkillSpec interprets a raw qvr.toml [skills] value (string or inline
// table, as decoded by go-toml) into a SkillSpec.
func parseSkillSpec(raw any) SkillSpec {
	switch v := raw.(type) {
	case string:
		return SkillSpec{Ref: v}
	case map[string]any:
		s := SkillSpec{}
		if r, ok := v["ref"].(string); ok {
			s.Ref = r
		}
		s.Targets = toStringSlice(v["targets"])
		return s
	}
	return SkillSpec{}
}

// toStringSlice normalises a decoded TOML array (which go-toml yields as []any)
// or an already-typed []string into []string. Returns nil for anything else.
func toStringSlice(raw any) []string {
	switch v := raw.(type) {
	case []string:
		if len(v) == 0 {
			return nil
		}
		return append([]string(nil), v...)
	case []any:
		if len(v) == 0 {
			return nil
		}
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
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
		p.Skills = make(map[string]any)
	}
	return p, nil
}

// MarshalProjectFile serializes a project file using the canonical on-disk TOML
// format. Callers comparing a planned write to prior bytes (idempotency) should
// use this so the comparison matches Write exactly.
//
// The [skills] block is emitted in two passes: everything except skills is
// marshaled normally, then the skills map is encoded with SetTablesInline so a
// per-skill target override renders as an inline table
// (`'reg/skill' = { ref = 'main', targets = ['x'] }`) while a bare ref stays a
// plain string. go-toml has no per-value marshal hook and TextMarshaler is
// string-only, so a single-pass struct marshal can't produce both shapes.
func MarshalProjectFile(p *ProjectFile) ([]byte, error) {
	// Pass 1: [project] + reserved sections (skills deliberately omitted).
	head := struct {
		Project  ProjectMeta    `toml:"project"`
		Registry map[string]any `toml:"registry,omitempty"`
		Plugins  map[string]any `toml:"plugins,omitempty"`
		Hooks    map[string]any `toml:"hooks,omitempty"`
		Mcp      map[string]any `toml:"mcp,omitempty"`
	}{Project: p.Project, Registry: p.Registry, Plugins: p.Plugins, Hooks: p.Hooks, Mcp: p.Mcp}
	data, err := toml.Marshal(head)
	if err != nil {
		return nil, fmt.Errorf("marshal project file: %w", err)
	}

	// Pass 2: the [skills] table, with inline tables for per-skill overrides.
	if len(p.Skills) > 0 {
		var sb bytes.Buffer
		enc := toml.NewEncoder(&sb)
		enc.SetTablesInline(true)
		if err := enc.Encode(p.Skills); err != nil {
			return nil, fmt.Errorf("marshal project file skills: %w", err)
		}
		if len(data) > 0 && data[len(data)-1] != '\n' {
			data = append(data, '\n')
		}
		data = append(data, "\n[skills]\n"...)
		data = append(data, sb.Bytes()...)
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

// PutSkill upserts a skill coordinate → ref entry, PRESERVING any existing
// per-skill target override. Use this when only the ref is known (e.g. projecting
// a lock ref into qvr.toml) so a hand-set or previously recorded target set is
// not clobbered.
func (p *ProjectFile) PutSkill(coordinate, ref string) {
	if p.Skills == nil {
		p.Skills = make(map[string]any)
	}
	if targets := p.SkillTargets(coordinate); len(targets) > 0 {
		p.Skills[coordinate] = map[string]any{"ref": ref, "targets": targets}
		return
	}
	p.Skills[coordinate] = ref
}

// PutSkillSpec upserts a skill coordinate with both its ref and an explicit
// per-skill target override. An empty targets set records the bare-string form
// (the skill follows [project].default-targets); a non-empty set records the
// inline-table form so the routing survives a front-door regenerate.
func (p *ProjectFile) PutSkillSpec(coordinate, ref string, targets []string) {
	if p.Skills == nil {
		p.Skills = make(map[string]any)
	}
	if len(targets) > 0 {
		p.Skills[coordinate] = map[string]any{"ref": ref, "targets": append([]string(nil), targets...)}
		return
	}
	p.Skills[coordinate] = ref
}

// GetSkill returns the ref for a coordinate, or ErrProjectSkillMissing.
func (p *ProjectFile) GetSkill(coordinate string) (string, error) {
	if raw, ok := p.Skills[coordinate]; ok {
		return parseSkillSpec(raw).Ref, nil
	}
	return "", fmt.Errorf("%w: %s", ErrProjectSkillMissing, coordinate)
}

// Skill returns the parsed spec for a coordinate and whether it is present.
func (p *ProjectFile) Skill(coordinate string) (SkillSpec, bool) {
	raw, ok := p.Skills[coordinate]
	if !ok {
		return SkillSpec{}, false
	}
	return parseSkillSpec(raw), true
}

// HasSkill reports whether a coordinate is declared.
func (p *ProjectFile) HasSkill(coordinate string) bool {
	_, ok := p.Skills[coordinate]
	return ok
}

// SkillRef returns the declared ref for a coordinate, or "" if absent.
func (p *ProjectFile) SkillRef(coordinate string) string {
	return parseSkillSpec(p.Skills[coordinate]).Ref
}

// SkillTargets returns the per-skill target override for a coordinate, or nil
// when the skill uses [project].default-targets (the bare-string form).
func (p *ProjectFile) SkillTargets(coordinate string) []string {
	raw, ok := p.Skills[coordinate]
	if !ok {
		return nil
	}
	return parseSkillSpec(raw).Targets
}

// RemoveSkill deletes a skill coordinate. A no-op (nil) if absent — removal
// must be idempotent so write-through on `qvr remove` never errors when the
// entry was non-portable (edit/link/local) and never had a coordinate.
func (p *ProjectFile) RemoveSkill(coordinate string) {
	delete(p.Skills, coordinate)
}

// SkillCoordinates returns the Skills entries' coordinates, sorted for
// stable iteration.
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
	for coord, raw := range p.Skills {
		if coord == "" {
			return errors.New("project file has a skill entry with an empty coordinate")
		}
		if parseSkillSpec(raw).Ref == "" {
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

// InferDefaultTargets scans projectRoot for existing agent skill directories
// (each Target.LocalDir) and returns the canonical target names that already
// have an on-disk presence, sorted and deduplicated. It is the engine behind
// `qvr init`'s auto-population of [project].default-targets.
//
// Many targets share a LocalDir — most notably ".agents/skills", used by
// codex/cursor/gemini AND the universal "project" target — so a present
// directory cannot be attributed to one agent. Inference dedupes by cleaned
// LocalDir and resolves each shared dir to the single canonical "project"
// (universal) target rather than emitting every agent that maps to it. A
// uniquely-owned dir resolves to its sole owner (.claude/skills→claude,
// .github/skills→copilot, .windsurf/skills→windsurf, …).
//
// Returns nil for a greenfield project with no agent dir on disk.
func InferDefaultTargets(projectRoot string) []string {
	owners := dirOwners()

	seen := make(map[string]struct{})
	var out []string
	for relDir, owner := range owners {
		info, err := os.Stat(filepath.Join(projectRoot, relDir))
		if err != nil || !info.IsDir() {
			continue
		}
		if _, dup := seen[owner]; dup {
			continue
		}
		seen[owner] = struct{}{}
		out = append(out, owner)
	}
	sort.Strings(out)
	return out
}

// dirOwners maps each cleaned LocalDir to the single canonical target that
// inference attributes it to. For a dir owned by multiple targets the universal
// "project" target wins; otherwise the alphabetically-first canonical name wins
// (deterministic, since TargetNames is sorted).
func dirOwners() map[string]string {
	owners := make(map[string]string)
	for _, name := range TargetNames() {
		key := filepath.Clean(Targets[name].LocalDir)
		switch cur, exists := owners[key]; {
		case !exists:
			owners[key] = name
		case name == "project":
			owners[key] = "project"
		case cur == "project":
			// keep the universal target
		default:
			// keep the alphabetically-first owner already recorded
		}
	}
	return owners
}
