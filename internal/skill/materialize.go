package skill

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/astra-sh/qvr/internal/model"
)

// ErrSubtreeAbsent means the skill's content subtree does not exist in the git
// tree at the materialized commit — almost always because the skill was added
// to the repo after that commit. Installer maps this to ErrSkillAbsentAtRef so
// the user gets an actionable message instead of a raw tree-walk failure.
var ErrSubtreeAbsent = errors.New("skill subtree absent at commit")

// BlobMaterializer is the seam through which a skill's blob bytes are written
// to disk. The default (nil) path is a plain stream copy; the reflink/hardlink
// content-store implementation (#205) plugs in here without the materializer
// knowing how the bytes land. A symlink blob never goes through this seam — it
// is created directly with os.Symlink, which reflink can't accelerate.
type BlobMaterializer interface {
	// WriteBlob materializes a regular/executable file at dst with perm mode.
	// read yields a fresh reader over the blob's bytes; implementations may
	// call it more than once (e.g. to populate a content store then clone it).
	// The parent directory of dst already exists.
	WriteBlob(dst string, mode os.FileMode, read func() (io.ReadCloser, error)) error
}

// Materializer writes a skill's content subtree directly from a bare repo's git
// objects into a destination directory, with NO git worktree and NO git
// subprocess. The bytes and modes it writes are chosen so that
// canonical.HashSubtreeFromDisk over the installed skill dir equals
// canonical.HashSubtreeAtCommit / HashScopedAtCommit for the same commit+scope
// — i.e. the worktree-free install hash-agrees with `qvr lock verify`.
//
// Blob is the #205 reflink seam; nil means plain stream copy.
type Materializer struct {
	Blob BlobMaterializer
}

// MaterializeSubtree writes the skill content for (repoPath @ commitish) into
// dest and returns the resolved full commit SHA. commitish is normally an
// already-resolved SHA, but a ref label (branch/tag/short-sha) is also accepted
// and resolved via go-git, so this is at least as robust as the prior git
// checkout. The on-disk layout is REPO-RELATIVE — every blob lands at
// dest/<repo-rel path>, exactly mirroring what the old `git clone --local` +
// sparse-checkout produced under the worktree root. Combined with
// EffectiveTarget pointing at dest/<subpath>, this keeps the disk hash identical
// to the git-tree hash.
//
// Scope mirrors model.SkillScopePaths and the prior sparse patterns:
//   - subpath != "" (non-root): walk the subtree at subpath → dest/<subpath>/...
//   - subpath == "" && !rootCoexists (lone root): walk the whole root tree.
//   - subpath == "" && rootCoexists: walk only SKILL.md + the recognized
//     content dirs (references/, scripts/, assets/); absent entries are skipped,
//     matching a sparse checkout that materializes nothing for them.
func (m *Materializer) MaterializeSubtree(repoPath, commitish, subpath string, rootCoexists bool, dest string) (string, error) {
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return "", fmt.Errorf("open repo: %w", err)
	}
	hash, err := resolveCommitish(repo, commitish)
	if err != nil {
		return "", err
	}
	c, err := repo.CommitObject(hash)
	if err != nil {
		return "", fmt.Errorf("load commit %s: %w", hash, err)
	}
	rootTree, err := c.Tree()
	if err != nil {
		return "", fmt.Errorf("load tree: %w", err)
	}

	// `.` denotes the repo root, same as an empty subpath (the "." → "" guard
	// mirrors canonical.hashSubtreeFromCommit).
	clean := strings.Trim(subpath, "/")
	if clean == "." {
		clean = ""
	}

	if clean == "" && rootCoexists {
		if err := m.writeScoped(rootTree, model.SkillScopePaths(clean, true), dest); err != nil {
			return "", err
		}
		return hash.String(), nil
	}

	subTree := rootTree
	if clean != "" {
		subTree, err = rootTree.Tree(clean)
		if err != nil {
			// Subtree not present at this commit — the skill doesn't exist here.
			return "", fmt.Errorf("%w: %q", ErrSubtreeAbsent, clean)
		}
	}
	// Write the subtree at its repo-relative location (dest/<clean>/...).
	if err := m.writeTreeFiles(subTree, filepath.Join(dest, filepath.FromSlash(clean))); err != nil {
		return "", err
	}
	return hash.String(), nil
}

// resolveCommitish turns a SHA or ref label into a commit hash. A full 40-hex
// string is taken verbatim (the common, already-resolved case); anything else
// is resolved through go-git's revision resolver, which handles branches, tags,
// and abbreviated SHAs.
func resolveCommitish(repo *gogit.Repository, commitish string) (plumbing.Hash, error) {
	if len(commitish) == 40 && plumbing.IsHash(commitish) {
		return plumbing.NewHash(commitish), nil
	}
	resolved, err := repo.ResolveRevision(plumbing.Revision(commitish))
	if err != nil || resolved == nil {
		return plumbing.ZeroHash, fmt.Errorf("resolve %q: %w", commitish, err)
	}
	return *resolved, nil
}

// HasGitDir reports whether path holds a .git entry — i.e. it is a legacy git
// worktree rather than a worktree-free content dir. Used to keep back-compat
// behavior (agent-view sanitization, git fast-forward update) for dirs left by
// older qvr versions.
func HasGitDir(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil
}

// writeScoped materializes a root-coexist skill: each scope entry is either a
// blob (SKILL.md) or a subtree (references/). Paths land repo-root-relative
// under dest. This parallels canonical.hashScopedFromCommit exactly so the
// digests agree.
func (m *Materializer) writeScoped(rootTree *object.Tree, scope []string, dest string) error {
	wrote := false
	for _, raw := range scope {
		p := strings.Trim(raw, "/")
		if p == "" || p == "." {
			continue
		}
		if f, ferr := rootTree.File(p); ferr == nil {
			if err := m.writeFile(f, filepath.Join(dest, filepath.FromSlash(p))); err != nil {
				return err
			}
			wrote = true
			continue
		}
		sub, terr := rootTree.Tree(p)
		if terr != nil {
			continue // absent scope entry — nothing to materialize, nothing to hash
		}
		if err := m.writeTreeFiles(sub, filepath.Join(dest, filepath.FromSlash(p))); err != nil {
			return err
		}
		wrote = true
	}
	if !wrote {
		return fmt.Errorf("%w: scoped root skill has no content at commit", ErrSubtreeAbsent)
	}
	return nil
}

// writeTreeFiles writes every blob in tree (recursively) under destRoot, at the
// blob's tree-relative path. Empty directories are not created — git doesn't
// track them and the disk hasher ignores them, so they can't affect identity.
func (m *Materializer) writeTreeFiles(tree *object.Tree, destRoot string) error {
	return tree.Files().ForEach(func(f *object.File) error {
		return m.writeFile(f, filepath.Join(destRoot, filepath.FromSlash(f.Name)))
	})
}

// writeFile materializes a single git blob at abs, preserving the git file mode
// so the disk hash agrees: symlink → 0120000 (blob bytes are the link target),
// executable → 0100755, regular → 0100644. Submodule/dir entries are skipped
// (a sparse checkout never materialized them either).
func (m *Materializer) writeFile(f *object.File, abs string) error {
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("create dir for %s: %w", f.Name, err)
	}
	switch f.Mode {
	case filemode.Symlink:
		target, err := blobBytes(f)
		if err != nil {
			return fmt.Errorf("read symlink %s: %w", f.Name, err)
		}
		// Refuse a link that would escape the materialized tree. We only ever
		// create the link node (never follow it), but an absolute or
		// parent-escaping target is a content red flag and could let a later
		// writer reach outside dest, so we reject it at install time.
		if t := string(target); filepath.IsAbs(t) || strings.HasPrefix(filepath.ToSlash(filepath.Clean(t)), "../") {
			return fmt.Errorf("refusing symlink %s with escaping target %q", f.Name, t)
		}
		return os.Symlink(string(target), abs)
	case filemode.Executable:
		if err := m.writeRegular(abs, 0o755, f); err != nil {
			return err
		}
		return os.Chmod(abs, 0o755)
	case filemode.Regular, filemode.Deprecated:
		return m.writeRegular(abs, 0o644, f)
	default:
		// filemode.Dir / filemode.Submodule and anything unexpected: skip, as
		// the prior sparse checkout did.
		return nil
	}
}

// writeRegular writes a regular/executable blob through the BlobMaterializer
// seam (reflink/content-store) when one is configured, else a plain stream
// copy. mode is the target perm; an executable also gets an explicit chmod by
// the caller in case the seam normalizes perms.
func (m *Materializer) writeRegular(abs string, mode os.FileMode, f *object.File) error {
	read := func() (io.ReadCloser, error) { return f.Reader() }
	if m.Blob != nil {
		return m.Blob.WriteBlob(abs, mode, read)
	}
	return copyBlobToFile(abs, mode, read)
}

// copyBlobToFile is the default (no content store) write: stream the blob into a
// fresh file with perm mode.
func copyBlobToFile(abs string, mode os.FileMode, read func() (io.ReadCloser, error)) error {
	r, err := read()
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	out, err := os.OpenFile(abs, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", abs, err)
	}
	if _, err := io.Copy(out, r); err != nil {
		_ = out.Close()
		return fmt.Errorf("write %s: %w", abs, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", abs, err)
	}
	return nil
}

// blobBytes reads a blob's full contents (used for symlink targets, which are
// small).
func blobBytes(f *object.File) ([]byte, error) {
	r, err := f.Reader()
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	return io.ReadAll(r)
}
