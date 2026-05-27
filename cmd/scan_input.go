package cmd

import (
	"archive/zip"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
)

// scanInputCleanup is set by [maybeResolveExternalInput] when the
// caller needs to clean up a temp directory after the scan finishes.
// Stored on the package so callers can defer it from runScan without
// passing it through the call stack.
var scanInputCleanup func() error

// maybeResolveExternalInput handles inputs that aren't a local
// directory and extends the SkillSpector input matrix:
//
//   - git URL (`https://github.com/owner/repo`, `git@…`, `ssh://`)
//     → shallow clone into a temp directory
//   - `.zip` archive → extract into a temp directory
//   - a single `SKILL.md` file → wrap its parent directory
//
// When the input is none of the above, the function returns an empty
// string and leaves the existing filesystem resolution in place.
//
// Cleanup is registered in [scanInputCleanup]; the caller is expected
// to defer it.
func maybeResolveExternalInput(arg string) (string, error) {
	if isGitURL(arg) {
		return cloneGitInput(arg)
	}
	if strings.HasSuffix(strings.ToLower(arg), ".zip") {
		return unzipInput(arg)
	}
	if strings.HasSuffix(arg, "SKILL.md") {
		// A bare SKILL.md path means "scan the skill in this file's
		// parent directory". We don't synthesise a temp dir for this
		// case — the parent dir already has the right shape.
		if _, err := os.Stat(arg); err == nil {
			parent := filepath.Dir(arg)
			if parent == "" || parent == "." {
				wd, err := os.Getwd()
				if err != nil {
					return "", fmt.Errorf("resolve cwd: %w", err)
				}
				parent = wd
			}
			return parent, nil
		}
	}
	return "", nil
}

// isGitURL returns true for arguments that look like a remote git
// repository spec. The check is intentionally conservative — local
// paths with colons (`./foo:bar`) are rare enough to ignore.
func isGitURL(arg string) bool {
	if strings.HasPrefix(arg, "git@") || strings.HasPrefix(arg, "ssh://") {
		return true
	}
	if u, err := url.Parse(arg); err == nil {
		switch u.Scheme {
		case "http", "https":
			host := strings.ToLower(u.Host)
			if strings.HasSuffix(arg, ".git") {
				return true
			}
			for _, h := range []string{"github.com", "gitlab.com", "bitbucket.org", "codeberg.org"} {
				if host == h {
					return true
				}
			}
		}
	}
	return false
}

func cloneGitInput(arg string) (string, error) {
	dir, err := os.MkdirTemp("", "qvr-scan-clone-*")
	if err != nil {
		return "", fmt.Errorf("mkdir tempdir: %w", err)
	}
	scanInputCleanup = func() error { return os.RemoveAll(dir) }
	_, err = git.PlainClone(dir, false, &git.CloneOptions{
		URL:   arg,
		Depth: 1,
	})
	if err != nil {
		_ = os.RemoveAll(dir)
		scanInputCleanup = nil
		return "", fmt.Errorf("clone %s: %w", arg, err)
	}
	return dir, nil
}

func unzipInput(arg string) (string, error) {
	dir, err := os.MkdirTemp("", "qvr-scan-zip-*")
	if err != nil {
		return "", fmt.Errorf("mkdir tempdir: %w", err)
	}
	scanInputCleanup = func() error { return os.RemoveAll(dir) }

	if err := extractZip(arg, dir); err != nil {
		_ = os.RemoveAll(dir)
		scanInputCleanup = nil
		return "", err
	}

	// If the zip contained a single top-level directory, descend into
	// it so the SKILL.md lookup hits the right place.
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) == 1 && entries[0].IsDir() {
		return filepath.Join(dir, entries[0].Name()), nil
	}
	return dir, nil
}

func extractZip(archive, dest string) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("open zip %s: %w", archive, err)
	}
	defer r.Close()

	cleanDest := filepath.Clean(dest) + string(os.PathSeparator)
	for _, f := range r.File {
		// Guard against path traversal — refuse entries whose
		// resolved path leaves the destination root.
		target := filepath.Join(dest, f.Name)
		if !strings.HasPrefix(filepath.Clean(target)+string(os.PathSeparator), cleanDest) &&
			filepath.Clean(target) != filepath.Clean(dest) {
			return fmt.Errorf("zip entry %q escapes destination", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := writeZipEntry(f, target); err != nil {
			return err
		}
	}
	return nil
}

func writeZipEntry(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, rc); err != nil { //nolint:gosec // bounded by zip member size
		return err
	}
	return nil
}
