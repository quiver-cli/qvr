package git_test

import (
	"strings"
	"testing"

	"github.com/raks097/quiver/internal/git"
)

func TestSanitizeURL(t *testing.T) {
	cases := []struct {
		name         string
		in           string
		wantClean    string
		wantHadCreds bool
		wantErr      bool
	}{
		{
			name:         "empty",
			in:           "",
			wantClean:    "",
			wantHadCreds: false,
		},
		{
			name:         "https no creds",
			in:           "https://github.com/foo/bar.git",
			wantClean:    "https://github.com/foo/bar.git",
			wantHadCreds: false,
		},
		{
			name:         "https with token as username",
			in:           "https://ghp_abc123@github.com/foo/bar.git",
			wantClean:    "https://github.com/foo/bar.git",
			wantHadCreds: true, // GitHub accepts this as a token; treat as creds
		},
		{
			name:         "https with user:token",
			in:           "https://user:ghp_abc123@github.com/foo/bar.git",
			wantClean:    "https://github.com/foo/bar.git",
			wantHadCreds: true,
		},
		{
			name:         "https with token only (token as password)",
			in:           "https://x-access-token:ghp_abc123@github.com/foo/bar.git",
			wantClean:    "https://github.com/foo/bar.git",
			wantHadCreds: true,
		},
		{
			name:         "http with creds",
			in:           "http://user:pw@host.internal/repo.git",
			wantClean:    "http://host.internal/repo.git",
			wantHadCreds: true,
		},
		{
			name:         "ssh protocol preserves user",
			in:           "ssh://git@github.com/foo/bar.git",
			wantClean:    "ssh://git@github.com/foo/bar.git",
			wantHadCreds: false,
		},
		{
			name:         "ssh protocol with password stripped, user kept",
			in:           "ssh://git:secret@github.com/foo/bar.git",
			wantClean:    "ssh://git@github.com/foo/bar.git",
			wantHadCreds: true,
		},
		{
			name:         "scp-style ssh unchanged",
			in:           "git@github.com:foo/bar.git",
			wantClean:    "git@github.com:foo/bar.git",
			wantHadCreds: false,
		},
		{
			name:         "scp-style with different user",
			in:           "myuser@gitlab.company.com:team/repo.git",
			wantClean:    "myuser@gitlab.company.com:team/repo.git",
			wantHadCreds: false,
		},
		{
			name:         "local path",
			in:           "/tmp/fixtures/bare.git",
			wantClean:    "/tmp/fixtures/bare.git",
			wantHadCreds: false,
		},
		{
			name:         "whitespace trimmed",
			in:           "  https://github.com/foo/bar.git  ",
			wantClean:    "https://github.com/foo/bar.git",
			wantHadCreds: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clean, hadCreds, err := git.SanitizeURL(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if clean != tc.wantClean {
				t.Errorf("clean = %q, want %q", clean, tc.wantClean)
			}
			if hadCreds != tc.wantHadCreds {
				t.Errorf("hadCreds = %v, want %v", hadCreds, tc.wantHadCreds)
			}
		})
	}
}

// TestSanitizeURL_NeverLeaksCreds is a belt-and-braces property check: for any
// URL we claim had credentials, the sanitised form must not contain the
// password substring anywhere.
func TestSanitizeURL_NeverLeaksCreds(t *testing.T) {
	secrets := []string{"ghp_deadbeef", "pat-12345", "s3cr3t!"}
	for _, secret := range secrets {
		url := "https://user:" + secret + "@github.com/foo/bar.git"
		clean, hadCreds, err := git.SanitizeURL(url)
		if err != nil {
			t.Fatalf("sanitize %s: %v", url, err)
		}
		if !hadCreds {
			t.Errorf("hadCreds false for %s", url)
		}
		if strings.Contains(clean, secret) {
			t.Errorf("clean %q still contains secret %q", clean, secret)
		}
	}
}

// TestSanitizeURL_Idempotent: sanitising an already-clean URL must be a no-op.
func TestSanitizeURL_Idempotent(t *testing.T) {
	inputs := []string{
		"https://github.com/foo/bar.git",
		"git@github.com:foo/bar.git",
		"ssh://git@github.com/foo/bar.git",
		"/tmp/bare.git",
	}
	for _, in := range inputs {
		once, _, err := git.SanitizeURL(in)
		if err != nil {
			t.Fatalf("sanitize %q: %v", in, err)
		}
		twice, _, err := git.SanitizeURL(once)
		if err != nil {
			t.Fatalf("sanitize %q: %v", once, err)
		}
		if once != twice {
			t.Errorf("not idempotent: %q → %q → %q", in, once, twice)
		}
	}
}
