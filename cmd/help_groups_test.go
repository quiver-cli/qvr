package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// TestPublishHelp_FlagsAreGrouped guards issue #109: publish's --help
// must render flags bucketed under Common / Authoring / Routing / Trust
// / Scope rather than a flat alphabetical wall. Asserts each group
// header appears AND a representative flag lands under it.
func TestPublishHelp_FlagsAreGrouped(t *testing.T) {
	buf := &bytes.Buffer{}
	publishCmd.SetOut(buf)
	publishCmd.SetErr(buf)
	if err := publishCmd.UsageFunc()(publishCmd); err != nil {
		t.Fatalf("UsageFunc: %v", err)
	}
	out := buf.String()

	// Each group header must appear.
	for _, header := range []string{"Common:", "Authoring:", "Routing:", "Trust:", "Scope:"} {
		if !strings.Contains(out, header) {
			t.Errorf("publish --help missing %q section. Output:\n%s", header, out)
		}
	}

	// Each representative flag must land under the correct group. Section
	// is the substring from the group header to the next blank line; we
	// can just check ordering via Index.
	cases := []struct {
		group string
		flag  string
	}{
		{"Common:", "--tag"},
		{"Common:", "--dry-run"},
		{"Authoring:", "--author"},
		{"Authoring:", "--branch"},
		{"Routing:", "--fork"},
		{"Routing:", "--migrate"},
		{"Routing:", "--layout"},
		{"Trust:", "--no-scan"},
		{"Trust:", "--allow-lockfile-heal"},
		{"Scope:", "--global"},
		{"Scope:", "--force"},
	}
	for _, tc := range cases {
		groupIdx := strings.Index(out, tc.group)
		if groupIdx == -1 {
			t.Errorf("missing %q in help output", tc.group)
			continue
		}
		// Bound the section by the next group header (or end of output).
		// We need to search for the flag WITHIN this window — the flag
		// also appears in the Long description above, which is why we
		// can't just use strings.Index on the whole output.
		sectionStart := groupIdx + len(tc.group)
		sectionEnd := len(out)
		for _, h := range []string{"Common:", "Authoring:", "Routing:", "Trust:", "Scope:", "Global Flags:"} {
			if h == tc.group {
				continue
			}
			if idx := strings.Index(out[sectionStart:], h); idx != -1 {
				if pos := sectionStart + idx; pos < sectionEnd {
					sectionEnd = pos
				}
			}
		}
		section := out[sectionStart:sectionEnd]
		if !strings.Contains(section, tc.flag) {
			t.Errorf("flag %s not rendered under %s. Section:\n%s", tc.flag, tc.group, section)
		}
	}
}
