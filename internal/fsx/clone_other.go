//go:build !darwin && !linux

package fsx

import "fmt"

// cloneFile has no copy-on-write primitive on this platform (e.g. Windows), so
// it always reports unsupported and the caller copies bytes.
func cloneFile(_, _ string) error {
	return fmt.Errorf("%w: no clone syscall on this platform", ErrCloneUnsupported)
}
