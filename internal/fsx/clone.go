// Package fsx provides filesystem primitives qvr needs that the standard
// library doesn't expose portably — chiefly copy-on-write file cloning
// (reflink), used to materialize skill blobs in O(metadata) instead of copying
// bytes (#205).
package fsx

import "errors"

// ErrCloneUnsupported is returned by CloneFile when a copy-on-write clone isn't
// possible for this (src, dst) pair: the platform lacks the syscall, the
// filesystem doesn't support reflinks (tmpfs, many CI volumes), or src and dst
// live on different devices. Callers treat it as a signal to fall back to a
// plain byte copy — it is never a hard failure.
var ErrCloneUnsupported = errors.New("copy-on-write clone unsupported")

// CloneFile creates dst as a copy-on-write clone of src. The clone shares src's
// data extents until one side is written, so it costs only metadata up front.
// dst must not already exist. On any filesystem/platform that can't honor the
// request it returns ErrCloneUnsupported (wrapped) and writes nothing, so the
// caller can fall back to a regular copy.
//
// Per-OS implementations live in clone_darwin.go (clonefile), clone_linux.go
// (FICLONE), and clone_other.go (always unsupported).
func CloneFile(src, dst string) error {
	return cloneFile(src, dst)
}
