package security

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassifyFile(t *testing.T) {
	cases := []struct {
		path  string
		want  string
		bin   bool
	}{
		{"SKILL.md", "markdown", false},
		{"scripts/install.sh", "shell", false},
		{"main.py", "python", false},
		{"app.ts", "typescript", false},
		{"page.tsx", "typescript", false},
		{"Dockerfile", "dockerfile", false},
		{"Makefile", "makefile", false},
		{"go.mod", "go-module", false},
		{"package.json", "npm-manifest", false},
		{"requirements.txt", "python-requirements", false},
		{"pyproject.toml", "python-project", false},
		{"unknown.xyz", "text", false},
		{"blob.bin", "binary", true},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			f := FileEntry{Path: c.path, IsBinary: c.bin}
			assert.Equal(t, c.want, ClassifyFile(f))
		})
	}
}

func TestCountLines(t *testing.T) {
	cases := []struct {
		name string
		f    FileEntry
		want int
	}{
		{"empty", FileEntry{Content: ""}, 0},
		{"one line no nl", FileEntry{Content: "hi"}, 1},
		{"one line nl", FileEntry{Content: "hi\n"}, 1},
		{"two lines", FileEntry{Content: "a\nb\n"}, 2},
		{"two lines no trailing nl", FileEntry{Content: "a\nb"}, 2},
		{"binary", FileEntry{IsBinary: true, Content: "any"}, 0},
		{"truncated", FileEntry{Truncated: true}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, countLines(c.f))
		})
	}
}

func TestComponentsFromFiles(t *testing.T) {
	files := []FileEntry{
		{Path: "SKILL.md", Content: "---\nname: x\ndescription: y\n---\n# hi\n", Size: 36},
		{Path: "scripts/install.sh", Content: "#!/usr/bin/env bash\necho hi\n", Size: 28, Mode: 0o755},
	}
	got := ComponentsFromFiles(files)
	if assert.Len(t, got, 2) {
		assert.Equal(t, "SKILL.md", got[0].Path)
		assert.Equal(t, "markdown", got[0].Type)
		assert.Equal(t, 5, got[0].Lines)
		assert.False(t, got[0].Executable)
		assert.Equal(t, int64(36), got[0].SizeBytes)

		assert.Equal(t, "scripts/install.sh", got[1].Path)
		assert.Equal(t, "shell", got[1].Type)
		assert.Equal(t, 2, got[1].Lines)
		assert.True(t, got[1].Executable, "executable bit on the shell script must surface")
		assert.Equal(t, int64(28), got[1].SizeBytes)
	}
}
