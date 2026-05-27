package cmd

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsGitURL(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"https://github.com/foo/bar", true},
		{"https://gitlab.com/foo/bar", true},
		{"https://example.com/repo.git", true},
		{"git@github.com:foo/bar.git", true},
		{"ssh://user@host/repo", true},
		{"./local/dir", false},
		{"/abs/path", false},
		{"my-skill", false},
		{"foo.zip", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			assert.Equal(t, c.want, isGitURL(c.in))
		})
	}
}

func TestUnzipInputExtractsSkill(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "skill.zip")
	mustWriteZip(t, zipPath, map[string]string{
		"my-skill/SKILL.md": "---\nname: my-skill\ndescription: zipped\n---\n",
	})

	out, err := unzipInput(zipPath)
	require.NoError(t, err)
	defer func() {
		if scanInputCleanup != nil {
			_ = scanInputCleanup()
			scanInputCleanup = nil
		}
	}()

	// single top-level dir → return that descent
	assert.True(t, filepath.Base(out) == "my-skill")
	_, err = os.Stat(filepath.Join(out, "SKILL.md"))
	assert.NoError(t, err, "extracted skill must contain SKILL.md")
}

func TestUnzipInputRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "evil.zip")
	mustWriteZip(t, zipPath, map[string]string{
		"../escape.txt": "no",
	})
	_, err := unzipInput(zipPath)
	require.Error(t, err, "must refuse zip entries that escape destination")
	if scanInputCleanup != nil {
		_ = scanInputCleanup()
		scanInputCleanup = nil
	}
}

func TestMaybeResolveExternalInput_PassThrough(t *testing.T) {
	out, err := maybeResolveExternalInput("./local/dir")
	require.NoError(t, err)
	assert.Empty(t, out, "local paths are passed through unchanged")
}

func TestMaybeResolveExternalInput_SkillMDParent(t *testing.T) {
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "SKILL.md")
	require.NoError(t, os.WriteFile(mdPath, []byte("---\nname: x\ndescription: y\n---\n"), 0o644))
	out, err := maybeResolveExternalInput(mdPath)
	require.NoError(t, err)
	assert.Equal(t, dir, out, "a single SKILL.md path resolves to its parent directory")
}

func mustWriteZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
}
