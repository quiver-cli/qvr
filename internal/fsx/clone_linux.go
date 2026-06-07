//go:build linux

package fsx

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// cloneFile uses the Linux FICLONE ioctl to make dst share src's extents
// (copy-on-write). Supported on btrfs, XFS (reflink=1), and a few others; on
// ext4, tmpfs, overlayfs, or across devices the ioctl returns
// ENOTSUP/EOPNOTSUPP/EXDEV/EINVAL, mapped to ErrCloneUnsupported so the caller
// falls back to a byte copy.
func cloneFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open src %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	// O_EXCL: dst must not already exist, matching CloneFile's contract and the
	// darwin clonefile semantics. Mode is provisional; the caller chmods to the
	// final perm after.
	out, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create dst %s: %w", dst, err)
	}

	if err := unix.IoctlFileClone(int(out.Fd()), int(in.Fd())); err != nil {
		_ = out.Close()
		_ = os.Remove(dst) // don't leave an empty file the caller would mistake for a clone
		if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) ||
			errors.Is(err, unix.EXDEV) || errors.Is(err, unix.EINVAL) {
			return fmt.Errorf("%w: FICLONE %s: %v", ErrCloneUnsupported, src, err)
		}
		return fmt.Errorf("FICLONE %s -> %s: %w", src, dst, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close dst %s: %w", dst, err)
	}
	return nil
}
