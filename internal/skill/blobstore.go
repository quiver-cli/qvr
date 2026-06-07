package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/astra-sh/qvr/internal/fsx"
	"github.com/astra-sh/qvr/internal/registry"
)

// cachedBlobMaterializer implements BlobMaterializer by routing every blob
// through a content-addressed store under ~/.quiver/cache/blobs and then
// REFLINKing (copy-on-write) the stored blob into the skill dir. Identical
// content across skills/versions is stored once; each materialized copy is an
// independent COW inode, so the per-skill read-only freeze (setSubtreeReadOnly)
// never bleeds across skills the way a hardlink's shared inode would. When the
// filesystem can't reflink (tmpfs, ext4, cross-device, Windows) it falls back to
// a plain byte copy — correct everywhere, fast where reflinks exist.
type cachedBlobMaterializer struct{}

// NewCachedBlobMaterializer returns the default reflink-backed BlobMaterializer.
func NewCachedBlobMaterializer() BlobMaterializer { return &cachedBlobMaterializer{} }

// WriteBlob ensures the blob is present in the content store (keyed by the
// sha256 of its raw bytes), then materializes it at dst via reflink, falling
// back to a copy. dst is finally chmod'd to mode.
func (c *cachedBlobMaterializer) WriteBlob(dst string, mode os.FileMode, read func() (io.ReadCloser, error)) error {
	storePath, err := c.ensureStored(read)
	if err != nil {
		return err
	}

	// Reflink the immutable store blob into place; on unsupported FS / cross
	// device, copy from the store. Either way dst is a fresh, independent file.
	// Only ErrCloneUnsupported is a copy-fallback signal — any other clone error
	// (e.g. a real I/O failure) must surface, not be masked by a copy attempt.
	if err := fsx.CloneFile(storePath, dst); err != nil {
		if !errors.Is(err, fsx.ErrCloneUnsupported) {
			return fmt.Errorf("materialize blob (clone failed): %w", err)
		}
		if copyErr := copyFileContents(storePath, dst); copyErr != nil {
			return fmt.Errorf("materialize blob (clone unsupported, copy failed): %w", copyErr)
		}
	}
	if err := os.Chmod(dst, mode); err != nil {
		return fmt.Errorf("chmod %s: %w", dst, err)
	}
	return nil
}

// ensureStored streams the blob into the content store if absent and returns its
// store path. Store entries are immutable and content-addressed: a write goes to
// a temp file and is atomically renamed in, so two processes racing the same
// blob both produce byte-identical content and the last rename harmlessly wins.
func (c *cachedBlobMaterializer) ensureStored(read func() (io.ReadCloser, error)) (string, error) {
	r, err := read()
	if err != nil {
		return "", err
	}
	defer func() { _ = r.Close() }()

	// Stream once into a temp file under the store root, hashing as we go, then
	// rename to the content-addressed name. Hashing while writing avoids a
	// second pass over the bytes.
	if err := os.MkdirAll(registry.BlobStoreRoot(), 0o755); err != nil {
		return "", fmt.Errorf("create blob store: %w", err)
	}
	tmp, err := os.CreateTemp(registry.BlobStoreRoot(), ".blob-*")
	if err != nil {
		return "", fmt.Errorf("create blob temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed away

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), r); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write blob temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close blob temp: %w", err)
	}

	storePath := registry.BlobStorePath(hex.EncodeToString(h.Sum(nil)))
	if _, statErr := os.Stat(storePath); statErr == nil {
		// Already stored — drop the temp (deferred) and reuse the existing blob.
		return storePath, nil
	}
	if err := os.MkdirAll(filepath.Dir(storePath), 0o755); err != nil {
		return "", fmt.Errorf("create blob shard: %w", err)
	}
	if err := os.Rename(tmpName, storePath); err != nil {
		// A racing writer may have created it between our Stat and Rename; reuse.
		if _, statErr := os.Stat(storePath); statErr == nil {
			return storePath, nil
		}
		return "", fmt.Errorf("commit blob to store: %w", err)
	}
	return storePath, nil
}

// copyFileContents copies src to a fresh dst (perms are set by the caller).
// Used as the reflink fallback.
func copyFileContents(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
