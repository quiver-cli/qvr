// Package manifest implements the plain-text portable skill manifest produced
// by `qvr export` and consumed by `qvr import`. See issue #127 for the format
// rationale.
//
// The on-disk shape is three whitespace-delimited columns plus optional
// trailing `--key=value` flags, one entry per line:
//
//	<repo-url>  <skill>  <version>  [--commit=<sha>] [--target=a,b] [--as=<name>] [--registry-alias=<name>]
//
// Lines starting with `#` are comments; blank lines are ignored. Alignment is
// purely cosmetic — the parser collapses runs of whitespace.
package manifest

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Entry is one row of the manifest.
type Entry struct {
	// RepoURL is the clone URL of the source registry. Required.
	RepoURL string
	// Skill is the skill name as published by the registry. Required.
	Skill string
	// Version is the requested ref — branch, tag, or commit. Required (use
	// the registry's default branch name explicitly rather than leaving it
	// blank; export never emits a blank version).
	Version string
	// Commit pins the entry to an exact SHA when set (emitted by `qvr export
	// --frozen`). Otherwise empty and the importer re-resolves Version.
	Commit string
	// Targets lists the agent targets the entry should install into (e.g.
	// "claude", "cursor"). Empty means "use the importer's default_target".
	Targets []string
	// Alias maps to `qvr add --as <name>` — install under a local name that
	// differs from Skill.
	Alias string
	// RegistryAlias is the local name to use when registering RepoURL during
	// import (matches `qvr registry add --name <alias>`). Empty means "infer
	// from URL" or "reuse an existing registration silently if the URL is
	// already registered under a different name".
	RegistryAlias string

	// Line is the 1-indexed source line number this entry parsed from. Set
	// by Parse; ignored by Format. Useful for "import: line N: …" diagnostics.
	Line int
}

// ParseError is a single per-line failure surfaced by Parse. Parse collects
// every malformed line and returns them together so a manifest with one bad
// row doesn't hide the rest of the diagnostics.
type ParseError struct {
	Line int
	Msg  string
}

// Error implements the error interface.
func (e ParseError) Error() string {
	return fmt.Sprintf("line %d: %s", e.Line, e.Msg)
}

// Parse reads a manifest from r and returns the parsed entries. Lines that
// can't be parsed are returned as ParseError values in the second slice; the
// caller decides whether to proceed with the partial result or fail. A nil
// reader error is returned alongside the partial result so callers can
// distinguish I/O failure from parse failure.
func Parse(r io.Reader) ([]Entry, []ParseError, error) {
	scanner := bufio.NewScanner(r)
	// Generous buffer: a single line carrying a long URL, all four flags, and
	// a 40-char commit hash should still fit comfortably. 1 MiB is overkill
	// but matches what the lockfile reader tolerates.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var entries []Entry
	var errs []ParseError
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		raw := scanner.Text()
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		entry, perr := parseLine(trimmed)
		if perr != "" {
			errs = append(errs, ParseError{Line: lineNum, Msg: perr})
			continue
		}
		entry.Line = lineNum
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return entries, errs, fmt.Errorf("read manifest: %w", err)
	}
	return entries, errs, nil
}

// parseLine splits a single non-blank, non-comment line into an Entry. Returns
// a non-empty error message on any structural failure; the caller wraps the
// message with the source line number.
func parseLine(line string) (Entry, string) {
	// Strip a trailing comment so `URL skill ref  # note` works.
	if i := strings.Index(line, " #"); i >= 0 {
		line = strings.TrimSpace(line[:i])
	} else if i := strings.Index(line, "\t#"); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}

	fields := strings.Fields(line)
	if len(fields) < 3 {
		return Entry{}, fmt.Sprintf("expected at least 3 fields (repo, skill, version); got %d", len(fields))
	}
	// The first three columns are positional; everything that follows is a
	// `--key=value` flag. We refuse any positional fourth field so users
	// catch typos like "v1.0 --frozen" (where --frozen would be silently
	// dropped if we tolerated positional extras).
	entry := Entry{
		RepoURL: fields[0],
		Skill:   fields[1],
		Version: fields[2],
	}
	if entry.RepoURL == "" || entry.Skill == "" || entry.Version == "" {
		return Entry{}, "repo URL, skill, and version are required"
	}
	for _, f := range fields[3:] {
		if !strings.HasPrefix(f, "--") {
			return Entry{}, fmt.Sprintf("unexpected positional field %q (flags must start with --)", f)
		}
		key, val, ok := strings.Cut(strings.TrimPrefix(f, "--"), "=")
		if !ok {
			return Entry{}, fmt.Sprintf("flag %q must be --key=value", f)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if val == "" {
			return Entry{}, fmt.Sprintf("flag --%s has empty value", key)
		}
		switch key {
		case "commit":
			entry.Commit = val
		case "target":
			// Comma-separated list per the issue's preferred shape. Trim each
			// segment so "claude, cursor" parses the same as "claude,cursor".
			parts := strings.Split(val, ",")
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p != "" {
					entry.Targets = append(entry.Targets, p)
				}
			}
			if len(entry.Targets) == 0 {
				return Entry{}, "flag --target has no non-empty values"
			}
		case "as":
			entry.Alias = val
		case "registry-alias":
			entry.RegistryAlias = val
		default:
			return Entry{}, fmt.Sprintf("unknown flag --%s", key)
		}
	}
	return entry, ""
}

// FormatOptions controls how Format renders the manifest.
type FormatOptions struct {
	// Header is an optional comment block written above the entries. Each
	// line is prefixed with "# " automatically; callers pass the bare text.
	// Blank string omits the header entirely.
	Header string
	// Align pads each column with spaces so the output is diff-friendly.
	// Off-by-default would force users to read about a flag; the only cost
	// of always aligning is a few trailing spaces, which `git diff` shows
	// the same as the unaligned form.
	Align bool
}

// Format writes entries to w using the manifest's column shape. Entries are
// written in input order — Format does not re-sort, so callers that want a
// deterministic on-disk order should sort first.
func Format(w io.Writer, opts FormatOptions, entries []Entry) error {
	bw := bufio.NewWriter(w)
	if opts.Header != "" {
		for line := range strings.SplitSeq(opts.Header, "\n") {
			if line == "" {
				if _, err := fmt.Fprintln(bw, "#"); err != nil {
					return err
				}
				continue
			}
			if _, err := fmt.Fprintf(bw, "# %s\n", line); err != nil {
				return err
			}
		}
	}

	// Compute column widths for alignment. Only the three positional columns
	// are aligned; flags vary too widely per row to pad usefully.
	urlW, skillW, versionW := 0, 0, 0
	if opts.Align {
		for _, e := range entries {
			if l := len(e.RepoURL); l > urlW {
				urlW = l
			}
			if l := len(e.Skill); l > skillW {
				skillW = l
			}
			if l := len(e.Version); l > versionW {
				versionW = l
			}
		}
	}

	for _, e := range entries {
		line := renderEntry(e, urlW, skillW, versionW)
		if _, err := fmt.Fprintln(bw, line); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// renderEntry produces a single manifest line. Pad columns to the supplied
// widths (zero means "no padding"). Trailing whitespace is trimmed so blank
// pads after the last printed column don't leak into the file.
func renderEntry(e Entry, urlW, skillW, versionW int) string {
	var b strings.Builder
	writeCol := func(s string, width int) {
		b.WriteString(s)
		if width > len(s) {
			b.WriteString(strings.Repeat(" ", width-len(s)))
		}
	}
	writeCol(e.RepoURL, urlW)
	b.WriteString("  ")
	writeCol(e.Skill, skillW)
	b.WriteString("  ")
	writeCol(e.Version, versionW)

	flags := e.flagsRendered()
	if flags != "" {
		b.WriteString("  ")
		b.WriteString(flags)
	}
	// Strip trailing spaces produced by the pad of the last column when no
	// flags follow.
	return strings.TrimRight(b.String(), " ")
}

// flagsRendered emits the entry's optional flags in a stable order so
// `qvr export` is deterministic across runs (otherwise map iteration would
// shuffle them and `git diff` would churn).
func (e Entry) flagsRendered() string {
	type kv struct{ k, v string }
	var pairs []kv
	if e.Commit != "" {
		pairs = append(pairs, kv{"commit", e.Commit})
	}
	if len(e.Targets) > 0 {
		// Copy + sort to keep the encoded form stable regardless of the
		// caller's slice order. Callers that want "claude,cursor" specifically
		// can pass them pre-sorted; sorting here guarantees the round-trip is
		// idempotent.
		t := append([]string(nil), e.Targets...)
		sort.Strings(t)
		pairs = append(pairs, kv{"target", strings.Join(t, ",")})
	}
	if e.Alias != "" {
		pairs = append(pairs, kv{"as", e.Alias})
	}
	if e.RegistryAlias != "" {
		pairs = append(pairs, kv{"registry-alias", e.RegistryAlias})
	}
	if len(pairs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, fmt.Sprintf("--%s=%s", p.k, p.v))
	}
	return strings.Join(parts, "  ")
}
