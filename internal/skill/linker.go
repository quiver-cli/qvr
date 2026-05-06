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

	// If a symlink already exists, check what it points at. Replace if wrong,
	// keep if right. This makes re-installs idempotent.
	if existing, err := os.Lstat(linkPath); err == nil {
		if existing.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("%w: %s", ErrSymlinkExists, linkPath)
		}
		current, err := os.Readlink(linkPath)
		if err != nil {
			return fmt.Errorf("read existing symlink: %w", err)
		}
		if current == absSkillDir {
			return nil
		}
		if err := os.Remove(linkPath); err != nil {
			return fmt.Errorf("replace existing symlink: %w", err)
		}
	}

	if err := os.Symlink(absSkillDir, linkPath); err != nil {
		return fmt.Errorf("create symlink: %w", err)
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

// VerifySymlink returns nil if linkPath is a symlink to an existing directory
// that contains SKILL.md. Callers use this during `qvr status` / `qvr list` to
// surface broken installs without performing any git operations.
func VerifySymlink(linkPath string) error {
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
	// Resolve the target relative to the symlink's own location so that
	// symlinks storing a relative path (as created by some tools) don't get
	// (mis)interpreted against the process's CWD.
	resolved, err := filepath.EvalSymlinks(linkPath)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrTargetNotExist, linkPath, err)
	}
	st, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("%w: %s -> %s: %v", ErrTargetNotExist, linkPath, resolved, err)
	}
	if !st.IsDir() {
		return fmt.Errorf("symlink target is not a directory: %s", resolved)
	}
	if _, err := os.Stat(filepath.Join(resolved, "SKILL.md")); err != nil {
		return fmt.Errorf("%w: %s", ErrTargetNotASkill, resolved)
	}
	return nil
}

// EffectiveTarget returns the absolute directory that a target symlink should
// point at for entry. Agent tools expect `.../skills/<name>/SKILL.md` directly
// under the symlinked directory, so the effective target is the worktree root
// joined with the skill's relative path inside the registry. Link installs
// store their absolute source in LinkTarget (or Path) and have no worktree.
// Keeping this as one helper prevents the three-way drift between
// `install`, `enable`, `doctor`, and `info` that shipped in v0.3.5.
func EffectiveTarget(entry *model.LockEntry) string {
	if entry == nil {
		return ""
	}
	if entry.Source == "link" {
		if entry.LinkTarget != "" {
			return entry.LinkTarget
		}
		return entry.Path
	}
	if entry.Worktree == "" {
		return ""
	}
	if entry.Path == "" {
		return entry.Worktree
	}
	return filepath.Join(entry.Worktree, entry.Path)
}

// VerifyTarget checks that a symlink points to the expected skillDir.
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
