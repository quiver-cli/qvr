package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/raks097/quiver/internal/model"
)

var (
	ErrSymlinkExists   = errors.New("symlink path already exists and is not a symlink")
	ErrSymlinkNotFound = errors.New("symlink not found")
	ErrSymlinkMismatch = errors.New("symlink target mismatch")
	ErrUnknownTarget   = errors.New("unknown agent target")
	ErrTargetNotASkill = errors.New("target path does not contain SKILL.md")
	ErrTargetNotExist  = errors.New("symlink target does not exist")
)

// ResolveTargetPath returns the directory where a symlink for skillName should
// be created for a given agent target. When global is true, it resolves the
// target's global directory under the user's home; otherwise the local
// (project-relative) directory rooted at projectRoot.
func ResolveTargetPath(target, skillName, projectRoot string, global bool) (string, error) {
	t, ok := model.Targets[target]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnknownTarget, target)
	}
	if global {
		expanded, err := expandHome(t.GlobalDir)
		if err != nil {
			return "", err
		}
		return filepath.Join(expanded, skillName), nil
	}
	return filepath.Join(projectRoot, t.LocalDir, skillName), nil
}

// CreateSymlink creates a symlink at linkPath pointing at skillDir. The parent
// directory is created if missing. An existing symlink to the same target is a
// no-op; an existing symlink to a different target is replaced; an existing
// regular file or directory errors out to protect user content.
func CreateSymlink(linkPath, skillDir string) error {
	info, err := os.Stat(skillDir)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrTargetNotExist, skillDir)
	}
	if !info.IsDir() {
		return fmt.Errorf("skill path %s is not a directory", skillDir)
	}
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
		return fmt.Errorf("%w: %s", ErrTargetNotASkill, skillDir)
	}

	absSkillDir, err := filepath.Abs(skillDir)
	if err != nil {
		return fmt.Errorf("resolve skill dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	// If a symlink already exists, check what it points at. A correct symlink
	// is a no-op (idempotent re-install). A non-symlink errors out to protect
	// user content. A wrong target falls through to the atomic replace below.
	if existing, err := os.Lstat(linkPath); err == nil {
		if existing.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("%w: %s", ErrSymlinkExists, linkPath)
		}
		if current, rerr := os.Readlink(linkPath); rerr == nil && current == absSkillDir {
			return nil
		}
	}

	// Create the symlink atomically: write it under a temp name in the same
	// directory, then rename over the destination. os.Rename of a symlink
	// over an existing symlink is atomic on POSIX, so a concurrent reader (a
	// coding agent following .claude/skills/<x> while `qvr update` repoints
	// it at a new immutable snapshot) never observes a missing link. The old
	// remove-then-symlink left a brief window where the link was absent.
	tmpLink := fmt.Sprintf("%s.qvrtmp.%d", linkPath, os.Getpid())
	_ = os.Remove(tmpLink)
	if err := os.Symlink(absSkillDir, tmpLink); err != nil {
		return fmt.Errorf("create symlink: %w", err)
	}
	if err := os.Rename(tmpLink, linkPath); err != nil {
		_ = os.Remove(tmpLink)
		return fmt.Errorf("install symlink: %w", err)
	}
	return nil
}

// RemoveSymlink removes a symlink at linkPath. Non-symlinks are never touched;
// a missing symlink returns ErrSymlinkNotFound so callers can treat it as a
// clean state.
func RemoveSymlink(linkPath string) error {
	info, err := os.Lstat(linkPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrSymlinkNotFound, linkPath)
		}
		return fmt.Errorf("stat symlink: %w", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("%w: %s is not a symlink", ErrSymlinkExists, linkPath)
	}
	if err := os.Remove(linkPath); err != nil {
		return fmt.Errorf("remove symlink: %w", err)
	}
	return nil
}

// EffectiveTarget returns the absolute directory that a target symlink should
// point at for entry. Agent tools expect `.../skills/<name>/SKILL.md` directly
// under the symlinked directory, so the effective target is the worktree root
// joined with the skill's relative path inside the registry. Link installs
// carry their absolute source in entry.Source and have no derived worktree.
// Edit installs (Mode == ModeEdit) live at <projectRoot>/<EditPath> — the
// canonical agent target dir is itself a real directory, with sibling targets
// repointed at it. Keeping this as one helper prevents the three-way drift
// between `install`, `enable`, `doctor`, and `info` that shipped in v0.3.5.
//
// projectRoot is consulted only for edit-mode entries. Callers that don't
// have a project root in scope (e.g. background diagnostics that only see
// the lock file) may pass "" — edit-mode entries then resolve to a relative
// path the caller must interpret against its own cwd.
func EffectiveTarget(entry *model.LockEntry, projectRoot string) string {
	if entry == nil {
		return ""
	}
	if entry.IsEdit() && entry.EditPath != "" {
		if projectRoot != "" && !filepath.IsAbs(entry.EditPath) {
			return filepath.Join(projectRoot, entry.EditPath)
		}
		return entry.EditPath
	}
	if entry.IsLink() {
		return entry.Source
	}
	worktree := EntryWorktreePath(entry)
	if worktree == "" {
		return ""
	}
	if entry.Path == "" {
		return worktree
	}
	return filepath.Join(worktree, entry.Path)
}

// VerifyTarget checks that a symlink points to the expected skillDir AND
// that the resolved target actually exists on disk. Catching dangling
// symlinks at the symlink check (rather than relying on the worktree
// check on the next line) makes `qvr doctor` honest at line-by-line
// granularity — issue #90.
func VerifyTarget(linkPath, skillDir string) error {
	info, err := os.Lstat(linkPath)
	if err != nil {
		return fmt.Errorf("stat symlink: %w", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("%w: %s is not a symlink", ErrSymlinkExists, linkPath)
	}
	target, err := os.Readlink(linkPath)
	if err != nil {
		return fmt.Errorf("read symlink: %w", err)
	}
	// Resolve relative symlink targets against the symlink's directory, not
	// the process CWD, before comparing to the expected absolute path.
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(linkPath), target)
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("resolve target: %w", err)
	}
	absExpected, err := filepath.Abs(skillDir)
	if err != nil {
		return fmt.Errorf("resolve expected: %w", err)
	}
	if absTarget != absExpected {
		return fmt.Errorf("%w: %s -> %s (want %s)", ErrSymlinkMismatch, linkPath, absTarget, absExpected)
	}
	// Stat the resolved target: a dangling symlink (target deleted out from
	// under it) previously passed VerifyTarget cleanly because we only
	// checked the link's name, not the existence of what it pointed at.
	// Issue #90.
	if _, err := os.Stat(absTarget); err != nil {
		return fmt.Errorf("%w: %s -> %s: %v", ErrTargetNotExist, linkPath, absTarget, err)
	}
	return nil
}

// VerifyDirContainsSkill reports whether dir is a real directory that holds
// a SKILL.md. Used by `qvr doctor` for mode:edit entries, where the
// canonical target dir IS a real directory (the edit copy) rather than a
// symlink — VerifyTarget would refuse it as `not a symlink` (issue #81).
func VerifyDirContainsSkill(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("stat %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		return fmt.Errorf("%w: %s", ErrTargetNotASkill, dir)
	}
	return nil
}

// expandHome replaces a leading "~/" with the user's home directory.
func expandHome(p string) (string, error) {
	if !strings.HasPrefix(p, "~/") && p != "~" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	if p == "~" {
		return home, nil
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~/")), nil
}
