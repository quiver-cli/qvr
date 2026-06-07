package skill

import (
	"io/fs"
	"os"
	"path/filepath"
)

// Immutability model ("uv for agent skills"): a shared install is the consume
// artifact and is frozen at download/publish; to modify a skill you `qvr edit`,
// which ejects a writable project-local copy. This keeps the bytes an agent
// reads through the symlink identical to what was scanned/verified, and closes
// the cross-project footgun where editing one project's shared worktree
// silently mutated every other project pinned to the same SHA.
//
// Mechanism: lock files read-only (0o444, or 0o555 when the file carries an
// executable bit) but keep directories traversable AND writable (0o755).
// Locking only files — not directories — is deliberate: on POSIX,
// deleting/replacing a file needs write on the parent directory, not the file,
// so this blocks accidental in-place overwrites (`os.WriteFile` over an
// installed file fails) while leaving go-git's remove-then-create checkout,
// `os.RemoveAll` teardown, and tooling that replaces whole files working
// without a write-perm round-trip. `.git` is always skipped so git metadata
// stays mutable; symlinks are left untouched (chmod would follow the link).
//
// The executable bit is preserved across the read-only/writable round-trip
// (issue #135): git checkout materialises a `100755` tree entry as a 0o755
// file, and the canonical subtree digest hashes that mode verbatim. Flattening
// every file to 0o444 stripped the exec bit on disk, so the verifier re-hashed
// the skill as `100644` and reported permanent drift for any skill shipping an
// executable script — a false integrity failure that broke `qvr sync --check`
// in CI. Keeping the bit also means an agent reading the script through the
// symlink still gets an executable file, which is the whole point of shipping
// one. The exec bit is part of the pinned supply-chain identity, not noise to
// normalise away.
//
// All chmod walks are best-effort — failures are non-fatal. Immutability is
// hardening layered on the load-bearing protections (reproducible PinCommit
// restore, atomic symlink swap, subtreeHash drift detection).

// setSubtreeReadOnly write-protects the files in an installed skill subtree,
// preserving the executable bit (0o555 for executables, 0o444 otherwise).
// Directories stay writable (0o755) so the worktree remains removable and
// checkout-able — see the package doc for why file-level locking is enough for
// a shared worktree.
func setSubtreeReadOnly(root string) {
	chmodSubtree(root, 0o444, 0o555, 0o755)
}

// subtreeFrozen reports whether an installed skill subtree is already
// write-protected, using the skill's own SKILL.md as a cheap proxy for the
// whole tree (it's always present and is the file the caller just stat-verified).
// A frozen install has SKILL.md at 0o444/0o555 — no owner-write bit. Used to
// skip a redundant recursive re-freeze on warm reuse of a shared content dir.
// Conservative: any stat failure returns false so the caller re-freezes.
func subtreeFrozen(skillDir string) bool {
	info, err := os.Stat(filepath.Join(skillDir, "SKILL.md"))
	if err != nil {
		return false
	}
	return info.Mode().Perm()&0o200 == 0
}

// setSubtreeWritable restores write permissions on a subtree (files 0o644,
// executables 0o755, dirs 0o755). Called when a previously frozen install
// transitions to mutable — `qvr edit` (eject copy), `CreateEditBranch`, or a
// fast-forward `qvr pull` that must rewrite working-tree files.
func setSubtreeWritable(root string) {
	chmodSubtree(root, 0o644, 0o755, 0o755)
}

// chmodSubtree walks root applying dirMode to directories, execFileMode to
// regular files that already carry an executable bit, and fileMode to all
// other regular files. Reading the current mode per file (rather than forcing
// a flat mode) is what preserves the exec bit across freeze/thaw — see the
// package doc and issue #135.
func chmodSubtree(root string, fileMode, execFileMode, dirMode os.FileMode) {
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries; best-effort
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			_ = os.Chmod(path, dirMode)
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		target := fileMode
		if info, ierr := d.Info(); ierr == nil && info.Mode().Perm()&0o111 != 0 {
			target = execFileMode
		}
		_ = os.Chmod(path, target)
		return nil
	})
}
