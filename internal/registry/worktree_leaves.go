package registry

import (
	"io/fs"
	"os"
	"path/filepath"

	"github.com/astra-sh/qvr/internal/config"
)

// WorktreeLeaves returns every on-disk worktree / content-dir leaf under the
// worktrees root — the set `qvr cache prune` compares against the reachable
// set (registry.Reachable) to find orphans.
//
// A leaf is found two ways, unioned and de-duplicated:
//
//   - Legacy git worktrees carry a `.git` entry (dir or file) and can sit
//     anywhere under the root, so a marker walk locates them.
//   - Worktree-free content dirs (#204) have NO `.git` — they're just the
//     materialized skill tree — so no marker identifies them. They live at the
//     deterministic `<registry>/<skill>/<sha>` depth that WorktreePath builds,
//     so they're enumerated structurally per configured registry.
//
// Before this, prune only knew the `.git` form, so a worktree-free content dir
// orphaned by a vanished project was never enumerated and so never reclaimed —
// `cache prune` reported "Removed 0 worktree(s)" and leaked it permanently
// (issue #221). The marker walk is kept as well so nothing the old enumeration
// found is now missed.
func WorktreeLeaves() []string {
	root := WorktreesRoot()
	if _, err := os.Stat(root); err != nil {
		return nil
	}

	seen := map[string]struct{}{}
	var leaves []string
	add := func(p string) {
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		leaves = append(leaves, p)
	}

	// Legacy `.git`-marked worktrees, anywhere under the root.
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if _, statErr := os.Stat(filepath.Join(path, ".git")); statErr == nil {
			add(path)
			return filepath.SkipDir // a worktree never nests another
		}
		return nil
	})

	// Worktree-free content dirs at <registry>/<skill>/<sha>, for each
	// configured registry (registry names may be `<org>/<repo>` — one slash —
	// which FromSlash maps to the nested on-disk path).
	cfg, err := config.Load()
	if err != nil {
		return leaves
	}
	for regName := range cfg.Registries {
		base := filepath.Join(root, filepath.FromSlash(regName))
		skillDirs, rdErr := os.ReadDir(base)
		if rdErr != nil {
			continue
		}
		for _, sd := range skillDirs {
			if !sd.IsDir() {
				continue
			}
			skillDir := filepath.Join(base, sd.Name())
			shaDirs, rdErr := os.ReadDir(skillDir)
			if rdErr != nil {
				continue
			}
			for _, shaDir := range shaDirs {
				if shaDir.IsDir() {
					add(filepath.Join(skillDir, shaDir.Name()))
				}
			}
		}
	}
	return leaves
}
