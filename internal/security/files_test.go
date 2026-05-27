package security

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWalkSkillSkipsGitAndOSMetadata(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "SKILL.md", "# x\n")
	mustWriteFile(t, dir, ".git/config", "should be skipped")
	mustWriteFile(t, dir, ".git/objects/abc", "skip")
	mustWriteFile(t, dir, ".DS_Store", "binary junk")
	mustWriteFile(t, dir, "Thumbs.db", "binary junk")
	mustWriteFile(t, dir, "scripts/run.sh", "#!/bin/bash\necho hi\n")

	entries, err := WalkSkill(dir)
	require.NoError(t, err)
	paths := pathsOf(entries)
	assert.ElementsMatch(t, []string{"SKILL.md", "scripts/run.sh"}, paths)
}

func TestWalkSkillFlagsExecutable(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "SKILL.md", "# x\n")
	mustWriteExecutable(t, dir, "scripts/run.sh", "#!/bin/bash\necho hi\n")

	entries, err := WalkSkill(dir)
	require.NoError(t, err)

	got := byPath(entries)
	skillEntry := got["SKILL.md"]
	scriptEntry := got["scripts/run.sh"]

	assert.False(t, skillEntry.Executable(), "SKILL.md should not be executable")
	assert.True(t, scriptEntry.Executable(), "scripts/run.sh should be executable")
}

func TestWalkSkillDetectsBinary(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "SKILL.md", "# x\n")
	mustWriteFile(t, dir, "blob.bin", "ELF\x00\x01\x02\x03binary content here")

	entries, err := WalkSkill(dir)
	require.NoError(t, err)
	got := byPath(entries)
	require.Contains(t, got, "blob.bin")
	assert.True(t, got["blob.bin"].IsBinary)
	assert.Empty(t, got["blob.bin"].Content)
	assert.False(t, got["SKILL.md"].IsBinary)
	assert.NotEmpty(t, got["SKILL.md"].Content)
}

func TestWalkSkillTruncatesOversizeFiles(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("a", maxScanBytes+1)
	mustWriteFile(t, dir, "huge.txt", big)
	mustWriteFile(t, dir, "SKILL.md", "# x\n")

	entries, err := WalkSkill(dir)
	require.NoError(t, err)
	got := byPath(entries)
	require.Contains(t, got, "huge.txt")
	assert.True(t, got["huge.txt"].Truncated)
	assert.Empty(t, got["huge.txt"].Content)
	assert.EqualValues(t, len(big), got["huge.txt"].Size)
}

func TestWalkSkillSortsByPath(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "z.md", "z\n")
	mustWriteFile(t, dir, "a.md", "a\n")
	mustWriteFile(t, dir, "scripts/m.sh", "m\n")

	entries, err := WalkSkill(dir)
	require.NoError(t, err)
	got := pathsOf(entries)
	assert.Equal(t, []string{"a.md", "scripts/m.sh", "z.md"}, got)
}

func TestWalkSkillUsesForwardSlashes(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, filepath.Join("nested", "file.md"), "x\n")
	entries, err := WalkSkill(dir)
	require.NoError(t, err)
	got := pathsOf(entries)
	for _, p := range got {
		assert.NotContains(t, p, `\`, "path should be forward-slashed")
	}
}

func TestWalkSkillRejectsBadInput(t *testing.T) {
	_, err := WalkSkill("")
	require.Error(t, err)

	_, err = WalkSkill("/nonexistent/path/that/does/not/exist")
	require.Error(t, err)
}

func TestIsBinary(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"plain text", "hello world\n", false},
		{"unicode text", "héllo wörld ✓\n", false},
		{"null byte", "abc\x00def", true},
		{"high bytes invalid utf8", "\xff\xfe\xfd\xfc plain ascii after", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, isBinary([]byte(c.in)))
		})
	}
}

func pathsOf(entries []FileEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Path)
	}
	return out
}

func byPath(entries []FileEntry) map[string]FileEntry {
	m := map[string]FileEntry{}
	for _, e := range entries {
		m[e.Path] = e
	}
	return m
}
