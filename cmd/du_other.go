//go:build !unix

package cmd

import "os"

// reclaimableFileSize falls back to the full file size on platforms without a
// portable hardlink count (e.g. Windows). `git clone --local` object-file
// hardlinking is a unix optimization; elsewhere the clone copies objects, so
// every file is unique to its worktree and the full size IS the honest reclaim
// figure. See the unix build for the rationale behind discounting shared
// (hardlinked) blocks (issue #158).
func reclaimableFileSize(info os.FileInfo) int64 {
	return info.Size()
}
