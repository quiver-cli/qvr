package cmd

import (
	"strings"
	"testing"
)

// TestRejectWebURL covers the GitHub/GitLab/Bitbucket web-browse URLs
// (`/tree/<ref>/<path>` and `/blob/<ref>/<path>`) that look clone-shaped
// but can't actually be cloned. Each rejection has to carry the
// "register the repo, then `qvr add <skill>`" hint so the user knows
// the v4 flow.
func TestRejectWebURL(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		wantErr  bool
		wantHint string
	}{
		{
			name:     "github tree URL rejected with registry-add hint",
			url:      "https://github.com/openclaw/skills/tree/main/skills/foo",
			wantErr:  true,
			wantHint: "qvr registry add",
		},
		{
			name:     "github blob URL rejected with registry-add hint",
			url:      "https://github.com/openclaw/skills/blob/main/skills/foo/SKILL.md",
			wantErr:  true,
			wantHint: "qvr registry add",
		},
		{
			name:    "gitlab tree URL rejected",
			url:     "https://gitlab.com/owner/repo/tree/main/sub",
			wantErr: true,
		},
		{
			name:    "bitbucket blob URL rejected",
			url:     "https://bitbucket.org/owner/repo/blob/main/sub",
			wantErr: true,
		},
		{
			name:    "plain clone URL passes through",
			url:     "https://github.com/owner/repo.git",
			wantErr: false,
		},
		{
			name:    "scp-style ssh URL passes through",
			url:     "git@github.com:owner/repo.git",
			wantErr: false,
		},
		{
			name:    "non-host path passes through (no host means rejectWebURL bails)",
			url:     "/tmp/local/bare.git",
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := rejectWebURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Fatalf("rejectWebURL(%q) err=%v, wantErr=%v", tt.url, err, tt.wantErr)
			}
			if tt.wantHint != "" && !strings.Contains(err.Error(), tt.wantHint) {
				t.Errorf("error %q missing hint %q", err.Error(), tt.wantHint)
			}
		})
	}
}
