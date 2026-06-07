package fsx_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/astra-sh/qvr/internal/fsx"
)

// TestCloneFile_ContentMatchesOrUnsupported asserts CloneFile either reproduces
// the source bytes exactly (where the FS supports reflinks) or reports
// ErrCloneUnsupported (where it doesn't) — never a silent partial/garbage clone.
func TestCloneFile_ContentMatchesOrUnsupported(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	want := []byte("copy-on-write payload\n")
	if err := os.WriteFile(src, want, 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	dst := filepath.Join(dir, "dst")

	err := fsx.CloneFile(src, dst)
	if err != nil {
		if errors.Is(err, fsx.ErrCloneUnsupported) {
			t.Skipf("reflink unsupported on this filesystem: %v", err)
		}
		t.Fatalf("CloneFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("clone content = %q, want %q", got, want)
	}
}

// TestCloneFile_IndependentInodes confirms a successful clone is an independent
// (copy-on-write) inode, not a hardlink — writing through one must not change
// the other. This is what lets qvr freeze a materialized blob read-only without
// touching the shared store entry.
func TestCloneFile_IndependentInodes(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("original"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	dst := filepath.Join(dir, "dst")
	if err := fsx.CloneFile(src, dst); err != nil {
		if errors.Is(err, fsx.ErrCloneUnsupported) {
			t.Skipf("reflink unsupported: %v", err)
		}
		t.Fatalf("CloneFile: %v", err)
	}
	if err := os.WriteFile(dst, []byte("changed-and-longer"), 0o644); err != nil {
		t.Fatalf("rewrite dst: %v", err)
	}
	srcData, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read src: %v", err)
	}
	if string(srcData) != "original" {
		t.Errorf("writing the clone mutated the source (not COW): src=%q", srcData)
	}
}

// TestCloneFile_RefusesExistingDst documents that CloneFile won't clobber an
// existing destination (matching darwin clonefile / linux O_EXCL semantics).
func TestCloneFile_RefusesExistingDst(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("a"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := os.WriteFile(dst, []byte("b"), 0o644); err != nil {
		t.Fatalf("write dst: %v", err)
	}
	if err := fsx.CloneFile(src, dst); err == nil {
		t.Errorf("expected CloneFile to refuse an existing dst")
	}
}
