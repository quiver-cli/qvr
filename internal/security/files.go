package security

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"unicode/utf8"

	"github.com/astra-sh/qvr/pkg/secretpatterns"
)

// DefaultMaxScanBytes is the per-file content cap used by [WalkSkill]
// when no caller override is in effect.
//
// Issue #44: the original 1 MiB ceiling silently broke the scanner's
// "every file is read as a string" premise — a single oversized blob
// inside a skill turned every detector blind. We raise the default to
// 10 MiB (large enough for vendored bundles, fixture corpora, and
// transcripts) and stream-scan files above the cap for credential
// prefixes so the most leak-prone shape still surfaces even when the
// full content was skipped.
const DefaultMaxScanBytes int64 = 10 << 20 // 10 MiB

// maxScanBytes is the live cap. Callers override it with
// [SetMaxScanBytes] (covers the `--max-file-bytes` CLI flag and the
// `QVR_MAX_FILE_BYTES` env). A value of 0 disables the cap entirely
// — the scanner reads every file regardless of size.
var maxScanBytes int64 = DefaultMaxScanBytes

// SetMaxScanBytes overrides the per-file cap. Pass 0 to disable.
// Negative values are clamped to 0. Returns the previous cap so test
// callers can restore it cleanly.
func SetMaxScanBytes(n int64) int64 {
	if n < 0 {
		n = 0
	}
	prev := maxScanBytes
	maxScanBytes = n
	return prev
}

// CurrentMaxScanBytes returns the cap currently in effect. Exported
// so the coverage check can include the exact byte count in its
// finding message regardless of overrides.
func CurrentMaxScanBytes() int64 { return maxScanBytes }

// init reads the QVR_MAX_FILE_BYTES env at process start so users can
// raise/disable the cap without a CLI flag. The flag still wins —
// cmd/scan.go calls SetMaxScanBytes after parsing args.
func init() {
	if env := os.Getenv("QVR_MAX_FILE_BYTES"); env != "" {
		if n, err := strconv.ParseInt(env, 10, 64); err == nil {
			SetMaxScanBytes(n)
		}
	}
}

// streamScanHardLimit is the absolute byte ceiling on the streaming
// credential-prefix scan we perform on oversize files. Anything above
// this we stop reading entirely to avoid eating an attacker's 100 GiB
// blob. Reviewer still sees the coverage warning attributing the gap.
const streamScanHardLimit int64 = 1 << 30 // 1 GiB

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
//
// Symlinks carry IsSymlink=true and never have Content populated — see
// the comment in [WalkSkill]. SymlinkTarget is the literal link
// target as stored on disk (without resolution). SymlinkBroken is set
// when the target cannot be Stat'd (dangling link or symlink cycle);
// callers use it to surface those anomalies separately from regular
// executable-bit findings (issue #40).
type FileEntry struct {
	Path          string      `json:"path"`
	Mode          fs.FileMode `json:"mode"`
	Size          int64       `json:"size"`
	Content       string      `json:"-"`
	IsBinary      bool        `json:"is_binary"`
	Truncated     bool        `json:"truncated,omitempty"`
	IsSymlink     bool        `json:"is_symlink,omitempty"`
	SymlinkTarget string      `json:"symlink_target,omitempty"`
	SymlinkBroken bool        `json:"symlink_broken,omitempty"`
	// OversizeSecretHits carries credential-prefix matches found by
	// the streaming scan of files above the per-file cap. The full
	// content was skipped, so these are the only secrets findings the
	// scanner has for that file. Empty for in-cap files (where the
	// regular secrets check sees full content). Issue #44.
	OversizeSecretHits []OversizeSecretHit `json:"oversize_secret_hits,omitempty"`
}

// OversizeSecretHit is one credential-prefix match recovered by
// streaming an oversize file. Line is 1-indexed.
type OversizeSecretHit struct {
	PatternName string `json:"pattern"`
	Line        int    `json:"line"`
}

// Executable reports whether any executable bit is set on the file.
// The scanner never runs anything; this exists so the permissions
// check can flag executables for human review.
//
// Symlinks always return false: on macOS/Linux, lstat reports the
// link's own mode (canonically 0o755 / 0o777), which says nothing
// about whether the target is executable. Reporting a symlink as
// executable on that basis is a documented false positive (#40).
func (f FileEntry) Executable() bool {
	if f.IsSymlink {
		return false
	}
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

		entry, skip, err := walkFileEntry(abs, path)
		if err != nil {
			return err
		}
		if skip {
			return nil
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

// walkFileEntry builds the FileEntry for a single regular file at path,
// relative to the scan root abs. It returns skip=true when the file
// produced no entry (currently never — reserved so the walker callback
// can stay branch-light); errors are already wrapped for the caller.
func walkFileEntry(abs, path string) (FileEntry, bool, error) {
	rel, err := filepath.Rel(abs, path)
	if err != nil {
		return FileEntry{}, false, err
	}
	rel = filepath.ToSlash(rel)

	// Use Lstat so symlinks come through as symlinks, not their
	// targets. This matters for the permissions check.
	fi, err := os.Lstat(path)
	if err != nil {
		return FileEntry{}, false, fmt.Errorf("lstat %s: %w", rel, err)
	}

	entry := FileEntry{
		Path: rel,
		Mode: fi.Mode(),
		Size: fi.Size(),
	}

	// Don't read symlinks; their content is the target path which
	// we'd misattribute as skill content.
	if fi.Mode()&os.ModeSymlink != 0 {
		fillSymlinkEntry(&entry, path)
		return entry, false, nil
	}

	if maxScanBytes > 0 && fi.Size() > maxScanBytes {
		entry.Truncated = true
		// Stream the file looking for credential prefixes only.
		// Other detectors (patterns, unicode, signatures) still
		// skip the file — the coverage check tells the user — but
		// the highest-stakes leak class (secrets) at least cannot
		// hide behind a 1-byte-over-cap pad. Issue #44.
		if hits, err := streamScanForSecrets(path); err == nil {
			entry.OversizeSecretHits = hits
		}
		return entry, false, nil
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return FileEntry{}, false, fmt.Errorf("read %s: %w", rel, err)
	}

	if isBinary(content) {
		entry.IsBinary = true
	} else {
		entry.Content = string(content)
	}

	return entry, false, nil
}

// fillSymlinkEntry populates the symlink-specific fields of entry for the
// link at path: its raw target and whether the target resolves. We never
// dereference the link to read content.
func fillSymlinkEntry(entry *FileEntry, path string) {
	entry.IsSymlink = true
	if target, err := os.Readlink(path); err == nil {
		entry.SymlinkTarget = target
	}
	if _, err := os.Stat(path); err != nil {
		// Stat follows symlinks, so an error here means the
		// target is missing or there's a cycle. Both belong on
		// the report (issue #40) — the permissions check
		// surfaces them as info findings.
		entry.SymlinkBroken = true
	}
}

// streamScanForSecrets opens path and runs the high-precision
// credential-prefix regexes line-by-line, returning one hit per
// (pattern, line) match. Stops reading after [streamScanHardLimit]
// bytes to avoid OOM on adversarial inputs.
//
// We only scan against [secretpatterns.CredentialPrefixes] (the
// vendor-anchored shapes — AWS, GitHub, OpenAI, etc.); the looser
// assignment-shape family is omitted to keep oversize-file false-
// positives down. Issue #44.
func streamScanForSecrets(path string) ([]OversizeSecretHit, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	type compiledPat struct {
		name string
		re   *regexp.Regexp
	}
	compiled := make([]compiledPat, 0, 16)
	for _, p := range secretpatterns.CredentialPrefixes() {
		r, err := p.Compile()
		if err != nil {
			continue
		}
		compiled = append(compiled, compiledPat{name: p.Name, re: r})
	}

	// Use a Scanner with a generous buffer so a single very long line
	// (a one-line minified bundle is the canonical case) doesn't
	// abort the scan with bufio.ErrTooLong.
	scanner := bufio.NewScanner(io.LimitReader(f, streamScanHardLimit))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var hits []OversizeSecretHit
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		for _, p := range compiled {
			if p.re.MatchString(line) {
				hits = append(hits, OversizeSecretHit{PatternName: p.name, Line: lineNum})
			}
		}
	}
	// Ignore scanner.Err() — partial coverage on a damaged file is
	// better than no coverage.
	return hits, nil
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
	if slices.Contains(probe, 0) {
		return true
	}
	return !utf8.Valid(probe)
}
