package git

import (
	"os"
	"path/filepath"
)

// EnclosingWorkTree walks up from dir looking for a `.git` entry — a directory
// for a normal repository, a file for a linked worktree or submodule — and
// returns the containing work-tree root. ok is false when no enclosing
// repository exists. dir need not exist yet; detection starts at the nearest
// existing ancestor semantics-free (a plain lexical walk).
func EnclosingWorkTree(dir string) (root string, ok bool) {
	d := filepath.Clean(dir)
	for {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d, true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", false
		}
		d = parent
	}
}
