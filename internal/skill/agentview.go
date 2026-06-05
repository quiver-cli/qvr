package skill

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/quiver-cli/qvr/internal/model"
)

// agentViewDir is the worktree-relative location of the sanitized directory a
// consumed root-layout skill exposes to agents. It deliberately lives under
// .git/ so it inherits, for free, every place that already skips .git: the
// canonical subtree hash (canonical.IsExcluded), the security scan gate
// (security/files.go), and go-git status. It is also removed automatically with
// the worktree and is SHA-keyed by virtue of living inside the SHA-keyed
// worktree — so it needs no separate lifecycle. See issue #154.
var agentViewDir = filepath.Join(".git", "qvr-view")

// IsRootLayoutPath reports whether a registry-relative path denotes the repo
// root — the layout where the whole worktree IS the skill (path "" or ".").
func IsRootLayoutPath(path string) bool {
	return path == "" || path == "."
}

// isConsumedRootLayout reports whether entry is a shared (registry-installed)
// root-layout skill. Only these expose the worktree's live .git/ through the
// agent symlink: a subdir skill points at a clean subtree, and edit/link
// installs have no shared worktree to sanitize.
func isConsumedRootLayout(entry *model.LockEntry) bool {
	if entry == nil || entry.IsEdit() || entry.IsLink() {
		return false
	}
	return IsRootLayoutPath(entry.Path)
}

// AgentLinkTarget returns the directory the agent-facing symlink
// (.claude/skills/<name>) should point at. It equals EffectiveTarget for every
// install shape EXCEPT a consumed root-layout skill, where it returns the
// sanitized view directory (under the worktree's .git/) that mirrors the skill
// content but omits .git — so an agent reading the skill never sees repo
// internals (issue #154).
//
// This is a PURE path computation (it does not create the view). Call
// MaterializeAgentView before (re)creating the symlink so the directory exists.
// EffectiveTarget stays the source of truth for content consumers (eject,
// publish, scan, hash), which must see the real worktree, not a view of
// symlinks.
func AgentLinkTarget(entry *model.LockEntry, projectRoot string) string {
	if !isConsumedRootLayout(entry) {
		return EffectiveTarget(entry, projectRoot)
	}
	worktree := EntryWorktreePath(entry)
	if worktree == "" {
		return ""
	}
	return filepath.Join(worktree, agentViewDir)
}

// MaterializeAgentView (re)builds the sanitized agent view for entry and returns
// the directory the symlink should point at. For non-root-layout entries it is
// a no-op returning EffectiveTarget. For a consumed root-layout skill it rebuilds
// worktree/.git/qvr-view from scratch as a directory of symlinks — one per
// top-level worktree entry except .git — and returns it. Rebuilding from scratch
// keeps the view exact when a re-link follows upstream content changing.
func MaterializeAgentView(entry *model.LockEntry, projectRoot string) (string, error) {
	if !isConsumedRootLayout(entry) {
		return EffectiveTarget(entry, projectRoot), nil
	}
	worktree := EntryWorktreePath(entry)
	if worktree == "" {
		return "", fmt.Errorf("agent view: no worktree for %s", entry.Name)
	}
	return buildAgentViewAt(worktree)
}

// buildAgentViewAt materializes worktree/.git/qvr-view as a directory of
// symlinks mirroring the worktree's top-level content minus .git, and returns
// its path. Symlink targets are absolute paths into the worktree, matching how
// the agent-facing symlink itself is written.
func buildAgentViewAt(worktree string) (string, error) {
	viewDir := filepath.Join(worktree, agentViewDir)
	if err := os.RemoveAll(viewDir); err != nil {
		return "", fmt.Errorf("agent view: clear %s: %w", viewDir, err)
	}
	if err := os.MkdirAll(viewDir, 0o755); err != nil {
		return "", fmt.Errorf("agent view: create %s: %w", viewDir, err)
	}
	entries, err := os.ReadDir(worktree)
	if err != nil {
		return "", fmt.Errorf("agent view: read worktree: %w", err)
	}
	for _, e := range entries {
		if e.Name() == ".git" {
			continue
		}
		src, err := filepath.Abs(filepath.Join(worktree, e.Name()))
		if err != nil {
			return "", fmt.Errorf("agent view: resolve %s: %w", e.Name(), err)
		}
		if err := os.Symlink(src, filepath.Join(viewDir, e.Name())); err != nil {
			return "", fmt.Errorf("agent view: link %s: %w", e.Name(), err)
		}
	}
	return viewDir, nil
}
