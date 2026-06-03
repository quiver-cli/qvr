package security

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDetectCapabilityLocations(t *testing.T) {
	files := []FileEntry{
		{Path: "main.py", Content: "line1\nimport subprocess\nsubprocess.run(['x'])\n"},
	}
	locs := DetectCapabilityLocations(files)
	site, ok := locs[CapShell]
	assert.True(t, ok)
	assert.Equal(t, "main.py", site.File)
	assert.Equal(t, 2, site.Line, "capability site should attribute to the first matching line")
}
