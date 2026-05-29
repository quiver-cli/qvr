package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/raks097/quiver/internal/canonical"
	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/registry"
)

var (
	ErrSkillNotFound    = errors.New("skill not found in any registry")
	ErrAlreadyInstalled = errors.New("skill is already installed")
	ErrInvalidReference = errors.New("invalid skill reference")
)

// InstallRequest describes a desired install.
type InstallRequest struct {
	Skill       string   // skill name, optionally with @version
	Targets     []string // e.g. []string{"claude", "cursor"}
	Global      bool
	ProjectRoot string
	LockPath    string // optional — DefaultLockPath is used when empty
	Force       bool   // allow overwriting an existing lock entry of the same name
	// Frozen pins installs to the lockfile's recorded state. The skill must
	// already have an entry; its Branch is reused and the computed subtree
	// hash must match the recorded VerificationRecord.SubtreeHash. Drift or
	// missing entries are hard errors.
	Frozen bool
}

// InstallResult holds the outcome for a single skill install.
type InstallResult struct {
	Name     string   `json:"name"`
	Registry string   `json:"registry"`
	Version  string   `json:"version"`
	Worktree string   `json:"worktree"`
	Targets  []string `json:"targets"`
	Commit   string   `json:"commit"`
}

// Installer orchestrates worktree + sparse checkout + symlinks + lock file.
type Installer struct {
	Registry *registry.Manager
	Worktree git.WorktreeManager
	Git      git.GitClient
}

// NewInstaller wires default dependencies.
func NewInstaller(reg *registry.Manager, wt git.WorktreeManager, gc git.GitClient) *Installer {
	return &Installer{Registry: reg, Worktree: wt, Git: gc}
}

// ParseReference splits "name@version" into its two parts. Version may be
// empty, in which case the registry's default branch is used at install time.
func ParseReference(ref string) (name, version string, err error) {
	if ref == "" {
		return "", "", fmt.Errorf("%w: empty reference", ErrInvalidReference)
	}
	parts := strings.SplitN(ref, "@", 2)
	name = strings.TrimSpace(parts[0])
	if name == "" {
		return "", "", fmt.Errorf("%w: empty name", ErrInvalidReference)
	}
	if len(parts) == 2 {
		version = strings.TrimSpace(parts[1])
	}
	return name, version, nil
}

// Install performs the full install flow. It is atomic at the worktree level:
// the worktree is created in a staging path, validated, and only renamed to
// the final path on success. Symlinks and lock file writes happen only after
// the worktree is in place.
func (in *Installer) Install(req InstallRequest) (*InstallResult, error) {
	name, version, err := ParseReference(req.Skill)
	if err != nil {
		return nil, err
	}
	if len(req.Targets) == 0 {
		return nil, fmt.Errorf("at least one --target is required")
	}
	for _, t := range req.Targets {
		if _, ok := model.Targets[t]; !ok {
			return nil, fmt.Errorf("%w: %s", ErrUnknownTarget, t)
		}
	}

	loc, err := in.Registry.FindSkill(name)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrSkillNotFound, name)
	}
	if version == "" {
		version = resolveDefaultRef(loc)
	}

	// --frozen pins to the lockfile: the entry must exist and its recorded
	// Branch/SubtreeHash become the install target. Captured here so the
	// drift check at the end can re-read the same recorded values.
	var frozenRef *model.LockEntry
	if req.Frozen {
		lp := req.LockPath
		if lp == "" {
			lp = model.DefaultLockPath(req.ProjectRoot, quiverHome(), req.Global)
		}
		existingLock, lerr := model.ReadLockFile(lp)
		if lerr != nil {
			return nil, fmt.Errorf("--frozen requires a readable lock file: %w", lerr)
		}
		existing, gerr := existingLock.Get(name)
		if gerr != nil {
			return nil, fmt.Errorf("--frozen: skill %q not present in lock file", name)
		}
		if existing.Ref != "" {
			version = existing.Ref
		}
		frozenRef = existing
	}

	// Conflict check: silently swapping the lock entry to a different ref
	// would contradict the "switching refs is a symlink repoint, not a
	// re-install" contract. Refuse and point at `qvr switch`. Idempotent
	// when the existing ref matches.
	if !req.Force {
		lp := req.LockPath
		if lp == "" {
			lp = model.DefaultLockPath(req.ProjectRoot, quiverHome(), req.Global)
		}
		if existingLock, lerr := model.ReadLockFile(lp); lerr == nil {
			if existing, gerr := existingLock.Get(name); gerr == nil && existing.Ref != "" && existing.Ref != version {
				return nil, fmt.Errorf("%s already installed at %s; use `qvr switch %s %s` to change refs, or `qvr remove %s` first (pass --force to override)",
					name, existing.Ref, name, version, name)
			}
		}
	}

	// Resolve the ref → full SHA against the bare clone so the worktree path
	// is SHA-keyed, not ref-keyed. Two projects pinning the same commit then
	// share one worktree even when they wrote different ref labels (one pinned
	// "main", the other "abc123"). Falls back to a degraded path using the ref
	// label when resolution fails — the install still succeeds and the lock
	// entry's Worktree field is still self-consistent; only the cross-project
	// share-by-SHA optimization is lost.
	resolvedSHA, sherr := in.Git.ResolveRef(loc.RepoPath, version)
	if sherr != nil || resolvedSHA == "" {
		resolvedSHA = version
	}

	// Staging path → final path. Worktree creation can fail mid-way (e.g., bad
	// ref), and we don't want a half-populated directory masquerading as an
	// installed skill. Stage in a sibling dir and rename at the end.
	finalPath := registry.WorktreePath(loc.RegistryName, name, registry.ShortSHA(resolvedSHA))
	stagingPath := finalPath + ".staging"
	_ = os.RemoveAll(stagingPath) // clear any stale staging from a prior crash

	if _, err := os.Stat(finalPath); err == nil {
		// Worktree already exists — reuse it. This makes `qvr install` idempotent
		// across multiple agent targets (install once, add cursor target, rerun
		// install).
	} else {
		if err := in.Worktree.Add(loc.RepoPath, stagingPath, version); err != nil {
			_ = os.RemoveAll(stagingPath)
			return nil, fmt.Errorf("create worktree: %w", err)
		}
		if err := in.Worktree.SetSparseCheckout(stagingPath, []string{loc.Entry.Path}); err != nil {
			_ = os.RemoveAll(stagingPath)
			return nil, fmt.Errorf("sparse checkout: %w", err)
		}
		if err := validateStagedSkill(stagingPath, loc.Entry.Path, name); err != nil {
			_ = os.RemoveAll(stagingPath)
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
			_ = os.RemoveAll(stagingPath)
			return nil, fmt.Errorf("create worktrees dir: %w", err)
		}
		if err := os.Rename(stagingPath, finalPath); err != nil {
			// Race: another process may have created finalPath between our
			// initial Stat and the Rename. If finalPath now exists, drop our
			// staged copy and reuse the winning one.
			if _, statErr := os.Stat(finalPath); statErr == nil {
				_ = os.RemoveAll(stagingPath)
			} else {
				_ = os.RemoveAll(stagingPath)
				return nil, fmt.Errorf("finalize worktree: %w", err)
			}
		}
	}

	skillDir := filepath.Join(finalPath, loc.Entry.Path)
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
		return nil, fmt.Errorf("skill path missing after checkout: %w", err)
	}

	// Create symlinks for every target. If any fails, roll back previously
	// created symlinks for this install to leave the filesystem consistent.
	var created []string
	for _, t := range req.Targets {
		linkPath, err := ResolveTargetPath(t, name, req.ProjectRoot, req.Global)
		if err != nil {
			rollbackLinks(created)
			return nil, err
		}
		if err := CreateSymlink(linkPath, skillDir); err != nil {
			rollbackLinks(created)
			return nil, fmt.Errorf("symlink %s: %w", t, err)
		}
		created = append(created, linkPath)
	}

	commit, _ := in.resolveCommit(finalPath)

	// Update lock file last — if it fails, everything else is still usable and
	// a subsequent install will reconcile state.
	lockPath := req.LockPath
	if lockPath == "" {
		lockPath = model.DefaultLockPath(req.ProjectRoot, quiverHome(), req.Global)
	}
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return nil, fmt.Errorf("read lock file: %w", err)
	}
	targets := req.Targets
	if existing, err := lock.Get(name); err == nil {
		targets = mergeTargets(existing.Targets, req.Targets)
	}
	subtreeHash, hashErr := ComputeSubtreeHash(finalPath, loc.Entry.Path)
	if hashErr != nil {
		// Hashing failure shouldn't block the install — we still want the
		// worktree and symlinks on disk so the user can use the skill. Leave
		// SubtreeHash empty; doctor/verify will flag the missing seal.
		subtreeHash = ""
	}

	entry := &model.LockEntry{
		Name:          name,
		Registry:      loc.RegistryName,
		Source:        loc.RegistryURL,
		Path:          loc.Entry.Path,
		Ref:           version,
		Commit:        commit,
		InstallCommit: commit,
		SubtreeHash:   subtreeHash,
		Targets:       targets,
	}

	// --frozen drift check: the just-installed worktree must hash to the
	// same SubtreeHash recorded in the prior lockfile entry. Mismatch
	// usually means the registry was force-pushed or the recorded entry
	// itself was tampered with — refuse the install rather than silently
	// rewriting history.
	if req.Frozen && frozenRef != nil && frozenRef.SubtreeHash != "" {
		if entry.SubtreeHash != frozenRef.SubtreeHash {
			return nil, fmt.Errorf("--frozen: subtree hash drift for %s (expected %s, got %s)",
				name, frozenRef.SubtreeHash, entry.SubtreeHash)
		}
	}

	lock.Put(entry)
	if err := lock.Write(); err != nil {
		return nil, fmt.Errorf("write lock file: %w", err)
	}

	return &InstallResult{
		Name:     name,
		Registry: loc.RegistryName,
		Version:  version,
		Worktree: finalPath,
		Targets:  targets,
		Commit:   commit,
	}, nil
}

// RestoreAll reinstalls every skill recorded in the lock file. Used by
// `qvr install` with no arguments to bring a fresh checkout up to state.
func (in *Installer) RestoreAll(req InstallRequest) ([]*InstallResult, error) {
	lockPath := req.LockPath
	if lockPath == "" {
		lockPath = model.DefaultLockPath(req.ProjectRoot, quiverHome(), req.Global)
	}
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return nil, fmt.Errorf("read lock file: %w", err)
	}
	if len(lock.Skills) == 0 {
		return nil, nil
	}
	var out []*InstallResult
	for _, entry := range lock.Entries() {
		skillRef := entry.Name
		if entry.Ref != "" {
			skillRef = entry.Name + "@" + entry.Ref
		}
		result, err := in.Install(InstallRequest{
			Skill:       skillRef,
			Targets:     entry.Targets,
			Global:      lock.IsGlobal(quiverHome()),
			ProjectRoot: req.ProjectRoot,
			LockPath:    lockPath,
			Frozen:      req.Frozen,
		})
		if err != nil {
			return out, fmt.Errorf("restore %s: %w", entry.Name, err)
		}
		out = append(out, result)
	}
	return out, nil
}

// Remove tears down a skill: remove symlinks, worktree, and lock entry. Any
// individual step failing still progresses through the rest so a partial
// installation doesn't get stuck.
func (in *Installer) Remove(name string, req InstallRequest) error {
	lockPath := req.LockPath
	if lockPath == "" {
		lockPath = model.DefaultLockPath(req.ProjectRoot, quiverHome(), req.Global)
	}
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return fmt.Errorf("read lock file: %w", err)
	}
	entry, err := lock.Get(name)
	if err != nil {
		return err
	}

	var firstErr error
	entryGlobal := lock.IsGlobal(quiverHome())
	for _, t := range entry.Targets {
		linkPath, err := ResolveTargetPath(t, name, req.ProjectRoot, entryGlobal)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := RemoveSymlink(linkPath); err != nil && !errors.Is(err, ErrSymlinkNotFound) {
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	if !entry.IsLink() {
		worktreePath := EntryWorktreePath(entry)
		if worktreePath != "" {
			if err := in.Worktree.Remove(worktreePath); err != nil && !errors.Is(err, git.ErrWorktreeNotFound) {
				if firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	if err := lock.Remove(name); err != nil && !errors.Is(err, model.ErrLockSkillMissing) {
		if firstErr == nil {
			firstErr = err
		}
	}
	if err := lock.Write(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// Link creates symlinks from a local skill directory into agent dirs. No
// worktree, no git, no lock-file bookkeeping unless the caller asked for it.
// This powers `qvr link` for local skill development.
func (in *Installer) Link(localPath string, req InstallRequest) (*InstallResult, error) {
	for _, t := range req.Targets {
		if _, ok := model.Targets[t]; !ok {
			return nil, fmt.Errorf("%w: %s", ErrUnknownTarget, t)
		}
	}
	abs, err := filepath.Abs(localPath)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}
	loaded, err := LoadFromPath(abs)
	if err != nil {
		return nil, err
	}
	// `qvr link` must respect the same spec rule `qvr validate` enforces: the
	// frontmatter name has to match the directory it lives in. Letting a
	// mismatch through creates a symlink that subsequent validate/doctor runs
	// immediately flag — silent drift we'd rather catch at link time.
	if result := Validate(loaded); !result.Valid {
		var lines []string
		for _, e := range result.Errors {
			lines = append(lines, e.Error())
		}
		return nil, fmt.Errorf("skill validation failed:\n  %s", strings.Join(lines, "\n  "))
	}
	name := loaded.Frontmatter.Name
	if name == "" {
		name = loaded.Name
	}

	// Conflict check: refuse to silently replace an existing entry of the
	// same name with a different on-disk target. Idempotent when the path
	// matches; --force needed to switch paths.
	lockPath := req.LockPath
	if lockPath == "" {
		lockPath = model.DefaultLockPath(req.ProjectRoot, quiverHome(), req.Global)
	}
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return nil, fmt.Errorf("read lock file: %w", err)
	}
	if existing, err := lock.Get(name); err == nil && !req.Force {
		// In v5, `source` carries the absolute path for link installs (and a
		// git URL for remote installs). A link install collides with an
		// existing entry only when the existing entry is *also* a link
		// pointing at the same path; otherwise refuse so we don't silently
		// shadow a remote-installed skill with a local symlink.
		if !existing.IsLink() || existing.Source != abs {
			sourceLabel := existing.Source
			if sourceLabel == "" {
				sourceLabel = "registry"
			}
			return nil, fmt.Errorf("skill %q already installed from %s; pass --force to relink",
				name, sourceLabel)
		}
	}

	var created []string
	for _, t := range req.Targets {
		linkPath, err := ResolveTargetPath(t, name, req.ProjectRoot, req.Global)
		if err != nil {
			rollbackLinks(created)
			return nil, err
		}
		if err := CreateSymlink(linkPath, abs); err != nil {
			rollbackLinks(created)
			return nil, fmt.Errorf("symlink %s: %w", t, err)
		}
		created = append(created, linkPath)
	}
	// Compute a subtree hash for the linked dir so drift detection still
	// works against the live source. Best-effort: a hashing failure leaves
	// the field empty and doctor/verify will flag it.
	linkSubtreeHash, _ := canonical.HashSubtreeFromDisk(abs)
	lock.Put(&model.LockEntry{
		Name:        name,
		Source:      abs,
		Ref:         "local",
		SubtreeHash: linkSubtreeHash,
		Targets:     req.Targets,
		InstalledAt: time.Now().UTC(),
	})
	if err := lock.Write(); err != nil {
		return nil, fmt.Errorf("write lock file: %w", err)
	}
	return &InstallResult{
		Name:     name,
		Version:  "link",
		Worktree: abs,
		Targets:  req.Targets,
	}, nil
}

func (in *Installer) resolveCommit(worktreePath string) (string, error) {
	if in.Git == nil {
		return "", nil
	}
	return in.Git.HeadCommit(worktreePath)
}

// validateStagedSkill loads the skill at the expected path inside the staged
// worktree and runs the standard validator. Refuses installs that would produce
// a symlink to a non-conformant skill.
//
// expectedName is the skill's canonical name from the registry index. The
// loader sets Skill.Name from the on-disk directory's basename, which for a
// layout-B repo (SKILL.md at the root, skillRelPath == ".") is the staging
// directory itself (e.g. `<reg>--<skill>--<ref>.staging`) — definitely not
// what the user wrote in `name:`. Overriding to expectedName before validation
// keeps the name↔dir match meaningful for layout A while letting layout B
// pass without leaking internal `.staging` paths to the user (bug #50).
func validateStagedSkill(stagingPath, skillRelPath, expectedName string) error {
	skillDir := filepath.Join(stagingPath, skillRelPath)
	loaded, err := LoadFromPath(skillDir)
	if err != nil {
		return fmt.Errorf("load staged skill: %w", err)
	}
	if expectedName != "" {
		loaded.Name = expectedName
	}
	if result := Validate(loaded); !result.Valid {
		var lines []string
		for _, e := range result.Errors {
			lines = append(lines, e.Error())
		}
		return fmt.Errorf("skill validation failed:\n  %s", strings.Join(lines, "\n  "))
	}
	return nil
}

func rollbackLinks(paths []string) {
	for _, p := range paths {
		_ = RemoveSymlink(p)
	}
}

func mergeTargets(existing, add []string) []string {
	set := make(map[string]struct{})
	for _, t := range existing {
		set[t] = struct{}{}
	}
	for _, t := range add {
		set[t] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// ApplySwitch refreshes the agent-target symlinks and verification record
// for an entry whose ref label changed in place. Used by `qvr edit` after
// CreateEditBranch mutates the worktree's HEAD onto a new branch at the same
// commit — same SHA, same on-disk path, just a new label in the lockfile.
//
// `qvr switch` / `qvr upgrade` no longer call this — they re-run Install with
// Force=true, which creates (or reuses) a fresh worktree at the new SHA's
// path and leaves the previous worktree in place for the shared-cache
// invariant to hold across projects (issue #52).
//
// global picks between project-local (`<project>/.claude/skills/`) and
// user-global (`~/.claude/skills/`) symlink targets — derive from the
// lock file's location via LockFile.IsGlobal.
func ApplySwitch(entry *model.LockEntry, projectRoot string, global bool) error {
	skillDir := EffectiveTarget(entry)
	for _, target := range entry.Targets {
		linkPath, err := ResolveTargetPath(target, entry.Name, projectRoot, global)
		if err != nil {
			return fmt.Errorf("resolve target %s: %w", target, err)
		}
		if err := CreateSymlink(linkPath, skillDir); err != nil {
			return fmt.Errorf("refresh symlink %s: %w", target, err)
		}
	}
	// Ref label or branch tip changed; the recorded subtree hash may no
	// longer match reality. Refresh it so the lockfile reflects the new
	// state. Failure is non-fatal — the post-switch symlinks are usable,
	// only the seal is stale until the next install/repair.
	_ = RefreshSubtreeHash(entry)
	return nil
}

// resolveDefaultRef picks the latest semver tag when any exist, else the
// registry's default branch. Non-semver tags are ignored so "bare install"
// rewards tag-using registries without surprising users with arbitrary
// moving labels like `latest` or `stable`.
func resolveDefaultRef(loc *registry.SkillLocation) string {
	if tag := LatestSemverTag(loc.Entry.Versions.Tags); tag != "" {
		return tag
	}
	return loc.DefaultBranch
}

// LatestSemverTag returns the highest-sorted semver tag from the given list,
// or "" when none qualify. Reuses model.SortVersions so precedence matches
// `qvr version list`.
func LatestSemverTag(tags []string) string {
	vl := &model.VersionList{}
	for _, t := range tags {
		if model.IsSemverTag(t) {
			vl.Tags = append(vl.Tags, model.Version{Ref: t, IsSemver: true})
		}
	}
	if len(vl.Tags) == 0 {
		return ""
	}
	model.SortVersions(vl, "")
	return vl.Tags[0].Ref
}

// quiverHome resolves the QUIVER_HOME override or falls back to ~/.quiver.
// Duplicated from config.Dir() to keep this package import-light in tests.
func quiverHome() string {
	if env := os.Getenv("QUIVER_HOME"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".quiver"
	}
	return filepath.Join(home, ".quiver")
}
