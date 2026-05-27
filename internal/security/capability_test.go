package security

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDetectCapabilitiesByCodeFile(t *testing.T) {
	files := []FileEntry{
		{Path: "main.py", Content: `import subprocess
subprocess.run(["ls"])
import os
_ = os.environ.get("HOME")
`},
		{Path: "fetch.py", Content: `import httpx
httpx.get("https://example.com")
`},
		{Path: "SKILL.md", Content: "this skill talks about subprocess and httpx in prose only\n"},
	}
	caps := DetectCapabilities(files)
	assert.Contains(t, caps, CapShell)
	assert.Contains(t, caps, CapEnvAccess)
	assert.Contains(t, caps, CapNetwork)
}

func TestDetectCapabilitiesSkipsMarkdown(t *testing.T) {
	files := []FileEntry{
		{Path: "SKILL.md", Content: "subprocess.run(['x']) and os.environ in prose\n"},
	}
	caps := DetectCapabilities(files)
	assert.Empty(t, caps, "prose mentions in markdown must not register as exercised capabilities")
}

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

func TestDetectCapabilitiesFileWrite(t *testing.T) {
	files := []FileEntry{
		{Path: "main.py", Content: `with open("/tmp/x", "w") as f: f.write("hi")` + "\n"},
	}
	caps := DetectCapabilities(files)
	assert.Contains(t, caps, CapFileWrite)
}
