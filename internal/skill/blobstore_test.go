package skill_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/astra-sh/qvr/internal/registry"
	"github.com/astra-sh/qvr/internal/skill"
)

func blobReader(b []byte) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
}

func storePathFor(content []byte) string {
	sum := sha256.Sum256(content)
	return registry.BlobStorePath(hex.EncodeToString(sum[:]))
}

// TestBlobStore_DedupAndPerDstMode materializes the same content into two
// destinations and confirms: the blob is stored exactly once (content-addressed
// dedup), both destinations carry the correct bytes, and each destination gets
// its own requested perm bits.
func TestBlobStore_DedupAndPerDstMode(t *testing.T) {
	testEnv(t)
	m := skill.NewCachedBlobMaterializer()
	content := []byte("shared blob content\n")
	store := storePathFor(content)

	dstA := filepath.Join(t.TempDir(), "a.txt")
	if err := m.WriteBlob(dstA, 0o644, blobReader(content)); err != nil {
		t.Fatalf("WriteBlob a: %v", err)
	}
	dstB := filepath.Join(t.TempDir(), "b.sh")
	if err := m.WriteBlob(dstB, 0o755, blobReader(content)); err != nil {
		t.Fatalf("WriteBlob b: %v", err)
	}

	if _, err := os.Stat(store); err != nil {
		t.Fatalf("blob not in store: %v", err)
	}
	// Dedup: exactly one blob file in the shard.
	shard, err := os.ReadDir(filepath.Dir(store))
	if err != nil {
		t.Fatalf("read shard: %v", err)
	}
	if len(shard) != 1 {
		t.Errorf("expected 1 stored blob after two identical writes, got %d", len(shard))
	}

	for _, d := range []string{dstA, dstB} {
		got, err := os.ReadFile(d)
		if err != nil || string(got) != string(content) {
			t.Errorf("dst %s content = %q (err %v), want %q", d, got, err, content)
		}
	}
	if fi, _ := os.Stat(dstA); fi.Mode().Perm()&0o111 != 0 {
		t.Errorf("dstA should be non-exec, got %v", fi.Mode())
	}
	if fi, _ := os.Stat(dstB); fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("dstB should be exec, got %v", fi.Mode())
	}
}

// TestBlobStore_FreezeDoesNotAffectStore is the immutability contract behind the
// reflink choice (over hardlinks): freezing a materialized copy read-only — what
// setSubtreeReadOnly does — must NOT change the shared store entry, because the
// materialized file is an independent inode (COW clone or copy), not a hardlink.
func TestBlobStore_FreezeDoesNotAffectStore(t *testing.T) {
	testEnv(t)
	m := skill.NewCachedBlobMaterializer()
	content := []byte("immutable payload\n")
	store := storePathFor(content)

	dst := filepath.Join(t.TempDir(), "x.txt")
	if err := m.WriteBlob(dst, 0o644, blobReader(content)); err != nil {
		t.Fatalf("WriteBlob: %v", err)
	}
	// Freeze the materialized copy read-only.
	if err := os.Chmod(dst, 0o444); err != nil {
		t.Fatalf("chmod dst: %v", err)
	}

	si, err := os.Stat(store)
	if err != nil {
		t.Fatalf("stat store: %v", err)
	}
	if si.Mode().Perm()&0o200 == 0 {
		t.Errorf("store entry lost its write bit after freezing a materialized copy: %v", si.Mode())
	}
	if got, _ := os.ReadFile(store); string(got) != string(content) {
		t.Errorf("store content changed after freeze: %q", got)
	}
}
