package security

import (
	"path"
	"strings"
)

// Component is a typed summary of one file inside a scanned skill.
//
// The JSON shape is part of the `qvr scan --output json` public
// contract — additive changes only. Consumers use this list to render
// a "what's in this skill?" sidebar without re-walking the directory.
type Component struct {
	Path       string `json:"path"`
	Type       string `json:"type"`
	Lines      int    `json:"lines"`
	Executable bool   `json:"executable"`
	SizeBytes  int64  `json:"size_bytes"`
}

// ComponentsFromFiles maps every FileEntry to a [Component]. Order is
// preserved — WalkSkill already sorts entries by path, so the
// resulting list is deterministic between scans.
func ComponentsFromFiles(files []FileEntry) []Component {
	out := make([]Component, 0, len(files))
	for _, f := range files {
		out = append(out, Component{
			Path:       f.Path,
			Type:       ClassifyFile(f),
			Lines:      countLines(f),
			Executable: f.Executable(),
			SizeBytes:  f.Size,
		})
	}
	return out
}

// countLines returns the number of newline-delimited lines in the file.
// Binary or truncated files report 0 — the count would be misleading
// (binary "lines" are not meaningful, and truncated files have no
// content to count). An empty text file reports 0.
func countLines(f FileEntry) int {
	if f.IsBinary || f.Truncated || f.Content == "" {
		return 0
	}
	n := strings.Count(f.Content, "\n")
	// A file that ends without a trailing newline still has a final
	// line; count it.
	if !strings.HasSuffix(f.Content, "\n") {
		n++
	}
	return n
}

// ClassifyFile returns a short, stable label for the FileEntry's
// content type. Used by [ComponentsFromFiles] and by SARIF/Markdown
// renderers that want to colour-code by language.
//
// The classifier is intentionally coarse: it leans on the filename
// (extension and well-known basenames) and falls back to "binary" or
// "text" for unknown files. We do not parse content beyond what
// WalkSkill already does (binary detection via NUL byte and UTF-8
// validity), so this stays cheap.
func ClassifyFile(f FileEntry) string {
	if f.IsBinary {
		return "binary"
	}
	base := strings.ToLower(path.Base(f.Path))
	if t, ok := basenameType[base]; ok {
		return t
	}
	ext := strings.ToLower(path.Ext(base))
	if t, ok := extType[ext]; ok {
		return t
	}
	return "text"
}

// basenameType maps known filenames (no extension required) to a type
// label. Catches things like `Makefile`, `Dockerfile`, `requirements.txt`
// before the extension lookup.
var basenameType = map[string]string{
	"skill.md":         "markdown",
	"readme.md":        "markdown",
	"makefile":         "makefile",
	"dockerfile":       "dockerfile",
	"requirements.txt": "python-requirements",
	"package.json":     "npm-manifest",
	"package-lock.json": "npm-lockfile",
	"go.mod":           "go-module",
	"go.sum":           "go-checksums",
	"pyproject.toml":   "python-project",
	"cargo.toml":       "cargo-manifest",
	"cargo.lock":       "cargo-lockfile",
	".env":             "env",
	".gitignore":       "gitignore",
}

// extType maps file extensions to a type label. Keep entries to a
// short list of "the things you'd realistically see in an agent
// skill"; an unknown ext falls back to "text".
var extType = map[string]string{
	".md":   "markdown",
	".rst":  "rst",
	".txt":  "text",
	".sh":   "shell",
	".bash": "shell",
	".zsh":  "shell",
	".fish": "shell",
	".py":   "python",
	".js":   "javascript",
	".ts":   "typescript",
	".tsx":  "typescript",
	".jsx":  "javascript",
	".go":   "go",
	".rb":   "ruby",
	".php":  "php",
	".pl":   "perl",
	".java": "java",
	".kt":   "kotlin",
	".rs":   "rust",
	".c":    "c",
	".h":    "c-header",
	".cpp":  "cpp",
	".hpp":  "cpp-header",
	".cs":   "csharp",
	".swift": "swift",
	".yaml": "yaml",
	".yml":  "yaml",
	".toml": "toml",
	".json": "json",
	".xml":  "xml",
	".html": "html",
	".css":  "css",
	".scss": "scss",
	".sql":  "sql",
	".ps1":  "powershell",
	".bat":  "batch",
	".cmd":  "batch",
	".aspx": "aspx",
	".jsp":  "jsp",
	".lock": "lockfile",
}
