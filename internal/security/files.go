package security

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"unicode/utf8"
)

// maxScanBytes is the per-file content cap. Files larger than this are
// recorded as FileEntry{Truncated: true} with empty Content so the
// permissions check can still flag oversize binaries without OOMing
// the scanner on a stray model checkpoint someone shipped in a skill.
const maxScanBytes = 1 << 20 // 1 MiB

// binaryProbeBytes is how many leading bytes WalkSkill inspects to
// decide whether a file is binary. UTF-8 validity over this prefix is
// the cheapest reliable signal — git uses the same heuristic.
const binaryProbeBytes = 512

// FileEntry is the scanner's view of one file inside a skill.
//
// Content is empty for binary files and for files that exceeded
// [maxScanBytes]; check those flags before searching Content. The Path
// uses forward slashes regardless of host OS so findings render
// identically on Windows and Unix.
type FileEntry struct {
	Path      string      `json:"path"`
	Mode      fs.FileMode `json:"mode"`
	Size      int64       `json:"size"`
	Content   string      `json:"-"`
	IsBinary  bool        `json:"is_binary"`
	Truncated bool        `json:"truncated,omitempty"`
}

// Executable reports whether any executable bit is set on the file.
// The scanner never runs anything; this exists so the permissions
// check can flag executables for human review.
func (f FileEntry) Executable() bool {
	return f.Mode&0o111 != 0
}

// WalkSkill walks dir and returns one FileEntry per regular file,
// sorted by path for deterministic check output. The walker skips
// .git directories and common OS metadata (.DS_Store, Thumbs.db) to
// match internal/skill.listFiles.
//
// Symlinks are returned as entries pointing at the link itself (no
// dereference) — both because we never want to follow a symlink into
// $HOME during a scan and so the permissions check can flag suspicious
// links.
func WalkSkill(dir string) ([]FileEntry, error) {
	if dir == "" {
		return nil, fmt.Errorf("scan dir is empty")
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve scan dir: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("stat scan dir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", abs)
	}

	var entries []FileEntry

	err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		base := d.Name()
		if base == ".DS_Store" || base == "Thumbs.db" {
			return nil
		}

		rel, err := filepath.Rel(abs, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		// Use Lstat so symlinks come through as symlinks, not their
		// targets. This matters for the permissions check.
		fi, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("lstat %s: %w", rel, err)
		}

		entry := FileEntry{
			Path: rel,
			Mode: fi.Mode(),
			Size: fi.Size(),
		}

		// Don't read symlinks; their content is the target path which
		// we'd misattribute as skill content.
		if fi.Mode()&os.ModeSymlink != 0 {
			entries = append(entries, entry)
			return nil
		}

		if fi.Size() > maxScanBytes {
			entry.Truncated = true
			entries = append(entries, entry)
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", rel, err)
		}

		if isBinary(content) {
			entry.IsBinary = true
		} else {
			entry.Content = string(content)
		}

		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func isBinary(content []byte) bool {
	probe := content
	if len(probe) > binaryProbeBytes {
		probe = probe[:binaryProbeBytes]
	}
	if len(probe) == 0 {
		return false
	}
	// A NUL byte in the first 512 bytes is the strongest single signal.
	for _, b := range probe {
		if b == 0 {
			return true
		}
	}
	return !utf8.Valid(probe)
}
