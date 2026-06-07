//go:build darwin

package fsx

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// cloneFile uses APFS clonefile(2) to create dst as a copy-on-write clone of
// src. clonefile fails (ENOTSUP/EXDEV/EINVAL/EEXIST) on non-APFS volumes,
// across devices, or when dst already exists — all mapped to
// ErrCloneUnsupported so the caller copies instead.
func cloneFile(src, dst string) error {
	err := unix.Clonefile(src, dst, 0)
	if err == nil {
		return nil
	}
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EXDEV) ||
		errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
		return fmt.Errorf("%w: clonefile %s: %v", ErrCloneUnsupported, src, err)
	}
	return fmt.Errorf("clonefile %s -> %s: %w", src, dst, err)
}
