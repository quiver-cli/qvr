package skill

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/registry"
)

var (
	ErrSkillNotFound    = errors.New("skill not found in any registry")
	ErrAlreadyInstalled = errors.New("skill is already installed")
	ErrInvalidReference = errors.New("invalid skill reference")
	// ErrSubpathMissing — the requested subdir doesn't exist in the repo at the
	// given ref. Surfaces from InstallFromSubdir when validateStagedSkill can't
	// stat the skill dir; the cmd layer remaps to a URL-domain message.
	ErrSubpathMissing = errors.New("subpath not found in repo at the given ref")
	// ErrRefNotFound — the requested ref (branch/tag/commit) doesn't exist on
	// origin. Surfaces from SubdirClone wrapped in "clone subdir: ..."; the
	// cmd layer detects and remaps.
	ErrRefNotFound = errors.New("ref not found")
)

// InstallRequest describes a desired install.
type InstallRequest struct {
	Skill       string   // skill name, optionally with @version
	Targets     []string // e.g. []string{"claude", "cursor"}
	Global      bool
	ProjectRoot string
	LockPath    string // optional — DefaultLockPath is used when empty
	Force       bool   // allow overwriting an existing lock entry of the same name
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
			if existing, gerr := existingLock.Get(name); gerr == nil && existing.Branch != "" && existing.Branch != version {
				return nil, fmt.Errorf("%s already installed at %s; use `qvr switch %s %s` to change refs, or `qvr remove %s` first (pass --force to override)",
					name, existing.Branch, name, version, name)
			}
		}
	}

	// Staging path → final path. Worktree creation can fail mid-way (e.g., bad
	// ref), and we don't want a half-populated directory masquerading as an
	// installed skill. Stage in a sibling dir and rename at the end.
	finalPath := registry.WorktreePath(loc.RegistryName, name, version)
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
		if err := validateStagedSkill(stagingPath, loc.Entry.Path); err != nil {
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
	lock.Put(&model.LockEntry{
		Name:     name,
		Registry: loc.RegistryName,
		Path:     loc.Entry.Path,
		Branch:   version,
		Commit:   commit,
		Worktree: finalPath,
		Targets:  targets,
		Global:   req.Global,
	})
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
		if entry.Branch != "" {
			skillRef = entry.Name + "@" + entry.Branch
		}
		result, err := in.Install(InstallRequest{
			Skill:       skillRef,
			Targets:     entry.Targets,
			Global:      entry.Global,
			ProjectRoot: req.ProjectRoot,
			LockPath:    lockPath,
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
	for _, t := range entry.Targets {
		linkPath, err := ResolveTargetPath(t, name, req.ProjectRoot, entry.Global)
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

	if entry.Worktree != "" {
		if err := in.Worktree.Remove(entry.Worktree); err != nil && !errors.Is(err, git.ErrWorktreeNotFound) {
			if firstErr == nil {
				firstErr = err
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

// SubdirInstallRequest describes an ad-hoc install of one skill that lives in
// a subdirectory of a remote git repo (e.g. a GitHub `/blob/<ref>/<path>`
// link). Unlike Install, this never touches the user's registry config — the
// bare clone lives in registry.SubdirRoot() and is owned by the lock entry
// rather than the registry list.
type SubdirInstallRequest struct {
	RepoURL     string   // e.g. "https://github.com/openclaw/skills.git"
	Ref         string   // branch, tag, or commit
	Subpath     string   // path inside the repo, e.g. "skills/jchopard69/x-article-editor"
	As          string   // optional override for the lock entry name (defaults to subpath leaf)
	Targets     []string // agent targets to symlink into
	Global      bool
	ProjectRoot string
	LockPath    string // optional — DefaultLockPath is used when empty
}

// InstallFromSubdir produces a shallow, sparse-checkout clone of req.RepoURL
// containing only req.Subpath at req.Ref, validates the skill there, links it
// into req.Targets, and records a lock entry. Skill name defaults to the
// subpath's leaf directory; pass req.As to override.
//
// The clone is stored under registry.SubdirRoot()/<slug>--<skill>--<ref>/.
// We use a partial+sparse clone (not bare-clone+worktree) so we don't pull
// down history or files outside the subpath — the standard registry path is
// optimised for "all skills, kept fresh," whereas this is "one skill, pin
// once." Re-running with the same (repo, skill, ref) reuses the existing
// clone.
func (in *Installer) InstallFromSubdir(ctx context.Context, req SubdirInstallRequest) (*InstallResult, error) {
	if req.RepoURL == "" {
		return nil, fmt.Errorf("repo URL is required")
	}
	if req.Ref == "" {
		return nil, fmt.Errorf("ref is required (branch, tag, or commit)")
	}
	subpath := strings.Trim(req.Subpath, "/")
	if subpath == "" {
		return nil, fmt.Errorf("subpath is required")
	}
	if len(req.Targets) == 0 {
		return nil, fmt.Errorf("at least one --target is required")
	}
	for _, t := range req.Targets {
		if _, ok := model.Targets[t]; !ok {
			return nil, fmt.Errorf("%w: %s", ErrUnknownTarget, t)
		}
	}

	cleanURL, _, err := git.SanitizeURL(req.RepoURL)
	if err != nil {
		return nil, fmt.Errorf("parse repo URL: %w", err)
	}
	slug := registry.URLToSlug(cleanURL)

	name := req.As
	if name == "" {
		parts := strings.Split(subpath, "/")
		name = parts[len(parts)-1]
	}

	// Each (repo, skill, ref) tuple gets its own sparse clone under SubdirRoot.
	// We reuse on collision so re-running `qvr add` is idempotent. Refs may
	// contain slashes (e.g. "feature/x"), so flatten them to "--" the same
	// way registry.WorktreePath does for the regular install path.
	dirName := strings.NewReplacer("/", "--", ":", "--").Replace
	finalPath := filepath.Join(registry.SubdirRoot(), fmt.Sprintf("%s--%s--%s", slug, dirName(name), dirName(req.Ref)))
	stagingPath := finalPath + ".staging"
	_ = os.RemoveAll(stagingPath)

	if _, err := os.Stat(finalPath); err == nil {
		// Reuse existing clone — caller can `qvr remove` then re-add to refresh.
	} else {
		if err := in.Git.SubdirClone(ctx, cleanURL, req.Ref, subpath, stagingPath); err != nil {
			_ = os.RemoveAll(stagingPath)
			// Raw go-git/`git checkout --detach` errors leak implementation
			// detail. Recognise the common "bad ref" shape and remap to a
			// user-friendly sentinel; everything else falls through.
			if isRefNotFound(err) {
				return nil, fmt.Errorf("%w: %s", ErrRefNotFound, req.Ref)
			}
			return nil, fmt.Errorf("clone subdir: %w", err)
		}
		if err := validateStagedSkill(stagingPath, subpath); err != nil {
			_ = os.RemoveAll(stagingPath)
			// "load staged skill: stat skill dir: stat .../staging/...: no
			// such file or directory" reads like an internal bug. The real
			// cause is "the subpath isn't in the repo at this ref" — remap.
			if isSubpathMissing(err) {
				return nil, fmt.Errorf("%w: %s", ErrSubpathMissing, subpath)
			}
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
			_ = os.RemoveAll(stagingPath)
			return nil, fmt.Errorf("create subdir clones dir: %w", err)
		}
		if err := os.Rename(stagingPath, finalPath); err != nil {
			if _, statErr := os.Stat(finalPath); statErr == nil {
				_ = os.RemoveAll(stagingPath)
			} else {
				_ = os.RemoveAll(stagingPath)
				return nil, fmt.Errorf("finalize subdir clone: %w", err)
			}
		}
	}

	skillDir := filepath.Join(finalPath, subpath)
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
		return nil, fmt.Errorf("skill path missing after checkout: %w", err)
	}

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

	// Subdir clones use partial fetch + sparse checkout, which has tripped up
	// go-git's ref-resolver in the past (HeadCommit returned no error but
	// reported an empty hash, leaving lock entries un-pinned). Shell out to
	// `git rev-parse HEAD` for a deterministic answer regardless of the
	// clone's filter / sparse state.
	commit := resolveSubdirCommit(ctx, finalPath)

	lockPath := req.LockPath
	if lockPath == "" {
		lockPath = model.DefaultLockPath(req.ProjectRoot, quiverHome(), req.Global)
	}
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return nil, fmt.Errorf("read lock file: %w", err)
	}
	if existing, err := lock.Get(name); err == nil {
		// A pre-existing entry can be reused only if it points at the same
		// upstream slug AND was managed by qvr (registry/subdir source).
		// Refuse otherwise rather than clobbering a link install or an entry
		// from a different repo.
		ownedByQvrAdd := existing.Source == "subdir" || existing.Source == "registry" || existing.Source == ""
		if !ownedByQvrAdd || existing.Registry != slug {
			sourceLabel := existing.Source
			if sourceLabel == "" {
				sourceLabel = "registry"
			}
			return nil, fmt.Errorf("skill %q already installed from %s (%s); pass --as <new-name> to disambiguate",
				name, sourceLabel, existing.Registry)
		}
	}
	targets := req.Targets
	if existing, err := lock.Get(name); err == nil {
		targets = mergeTargets(existing.Targets, req.Targets)
	}
	lock.Put(&model.LockEntry{
		Name:     name,
		Registry: slug,
		RepoURL:  cleanURL,
		Path:     subpath,
		Branch:   req.Ref,
		Commit:   commit,
		Worktree: finalPath,
		Targets:  targets,
		Global:   req.Global,
		// "subdir" distinguishes ad-hoc URL installs from registry-driven ones
		// so `qvr doctor` doesn't expect a matching config.Registries entry
		// and `qvr outdated` reaches for RepoURL instead.
		Source: "subdir",
	})
	if err := lock.Write(); err != nil {
		return nil, fmt.Errorf("write lock file: %w", err)
	}
	return &InstallResult{
		Name:     name,
		Registry: slug,
		Version:  req.Ref,
		Worktree: finalPath,
		Targets:  targets,
		Commit:   commit,
	}, nil
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
		existingPath := existing.LinkTarget
		if existingPath == "" {
			existingPath = existing.Path
		}
		if existing.Source != "link" || existingPath != abs {
			sourceLabel := existing.Source
			if sourceLabel == "" {
				sourceLabel = "registry"
			}
			return nil, fmt.Errorf("skill %q already installed from %s (%s); pass --force to relink",
				name, sourceLabel, existingPath)
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
	lock.Put(&model.LockEntry{
		Name:        name,
		Registry:    "",
		Path:        abs,
		Worktree:    "",
		Targets:     req.Targets,
		Global:      req.Global,
		Source:      "link",
		LinkTarget:  abs,
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

// resolveSubdirCommit returns the HEAD commit of a subdir clone. It shells
// out to `git rev-parse HEAD` rather than using go-git because partial+sparse
// clones have, in practice, returned an empty hash through go-git's
// PlainOpen → Head() path, leaving lock entries un-pinned and breaking
// downstream `qvr outdated`. Returns "" on any error so the caller falls
// back to recording the entry without a commit (still better than failing
// the install).
func resolveSubdirCommit(ctx context.Context, repoPath string) string {
	out, err := git.RunInDir(ctx, repoPath, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// validateStagedSkill loads the skill at the expected path inside the staged
// worktree and runs the standard validator. Refuses installs that would produce
// a symlink to a non-conformant skill.
func validateStagedSkill(stagingPath, skillRelPath string) error {
	skillDir := filepath.Join(stagingPath, skillRelPath)
	loaded, err := LoadFromPath(skillDir)
	if err != nil {
		return fmt.Errorf("load staged skill: %w", err)
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

// isRefNotFound recognises the error shapes go-git / shell-git produce when
// the requested branch/tag/commit doesn't exist on origin. We pattern-match
// on substrings because the underlying types vary by code path (go-git's
// transport.ErrEmptyRemoteRepository, plumbing.ErrReferenceNotFound, or
// shelled-out `git`'s stderr like "fatal: git checkout: --detach does not
// take a path argument 'X'"). Stable enough across versions for a CLI hint.
func isRefNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"reference not found",
		"couldn't find remote ref",
		"--detach does not take a path argument",
		"did not match any file(s) known to git",
		"unknown revision",
		"pathspec",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// isSubpathMissing recognises the error shape LoadFromPath emits when the
// requested skill directory doesn't exist inside a sparse clone. The exact
// wrapping is "load staged skill: stat skill dir: stat <path>: no such file
// or directory".
func isSubpathMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "stat skill dir") && strings.Contains(msg, "no such file or directory")
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

// ApplySwitch finalizes a ref change after Syncer.Switch has moved the
// worktree's git HEAD. It renames the on-disk worktree directory to the path
// matching the new ref and repoints every target symlink for the skill.
// Callers own lock.Put + lock.Write; this helper only touches the filesystem.
// entry.Branch must already reflect the new ref when called.
func ApplySwitch(entry *model.LockEntry, projectRoot string) error {
	newPath := registry.WorktreePath(entry.Registry, entry.Name, entry.Branch)
	oldPath := entry.Worktree
	if newPath != oldPath {
		_, statErr := os.Stat(newPath)
		switch {
		case statErr == nil:
			// A worktree at the target path already exists (prior install of
			// the same ref). Drop the stale old directory and point at it.
			if err := os.RemoveAll(oldPath); err != nil {
				return fmt.Errorf("remove old worktree %s: %w", oldPath, err)
			}
		case os.IsNotExist(statErr):
			if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
				return fmt.Errorf("mkdir for new worktree: %w", err)
			}
			if err := os.Rename(oldPath, newPath); err != nil {
				return fmt.Errorf("rename worktree: %w", err)
			}
		default:
			return fmt.Errorf("stat new worktree path %s: %w", newPath, statErr)
		}
		entry.Worktree = newPath
	}

	skillDir := EffectiveTarget(entry)
	for _, target := range entry.Targets {
		linkPath, err := ResolveTargetPath(target, entry.Name, projectRoot, entry.Global)
		if err != nil {
			return fmt.Errorf("resolve target %s: %w", target, err)
		}
		if err := CreateSymlink(linkPath, skillDir); err != nil {
			return fmt.Errorf("refresh symlink %s: %w", target, err)
		}
	}
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
