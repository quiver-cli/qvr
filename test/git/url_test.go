package gittests

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

func TestParseSubdirURL(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		wantRepo    string
		wantRef     string
		wantSubpath string
		wantErr     bool
	}{
		{
			name:        "github blob URL with trailing slash",
			in:          "https://github.com/openclaw/skills/blob/main/skills/jchopard69/x-article-editor/",
			wantRepo:    "https://github.com/openclaw/skills.git",
			wantRef:     "main",
			wantSubpath: "skills/jchopard69/x-article-editor",
		},
		{
			name:        "github tree URL",
			in:          "https://github.com/owner/repo/tree/v1.2.3/skills/foo",
			wantRepo:    "https://github.com/owner/repo.git",
			wantRef:     "v1.2.3",
			wantSubpath: "skills/foo",
		},
		{
			name:    "github URL without subdir",
			in:      "https://github.com/owner/repo",
			wantErr: true,
		},
		{
			name:    "non-github subdir-shaped URL",
			in:      "https://gitlab.com/owner/repo/-/tree/main/skills/foo",
			wantErr: true,
		},
		{
			name:    "ssh URL never matches subdir",
			in:      "git@github.com:owner/repo.git",
			wantErr: true,
		},
		{
			name:        "github trailing .git stripped",
			in:          "https://github.com/owner/repo.git/blob/main/skills/foo",
			wantRepo:    "https://github.com/owner/repo.git",
			wantRef:     "main",
			wantSubpath: "skills/foo",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := git.ParseSubdirURL(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.RepoURL != tc.wantRepo {
				t.Errorf("RepoURL = %q, want %q", got.RepoURL, tc.wantRepo)
			}
			if got.Ref != tc.wantRef {
				t.Errorf("Ref = %q, want %q", got.Ref, tc.wantRef)
			}
			if got.Subpath != tc.wantSubpath {
				t.Errorf("Subpath = %q, want %q", got.Subpath, tc.wantSubpath)
			}
		})
	}
}

func TestSubdirURL_LeafName(t *testing.T) {
	cases := []struct {
		subpath, want string
	}{
		{"skills/jchopard69/x-article-editor", "x-article-editor"},
		{"skills/jchopard69/x-article-editor/", "x-article-editor"},
		{"foo", "foo"},
		{"", ""},
	}
	for _, tc := range cases {
		s := &git.SubdirURL{Subpath: tc.subpath}
		if got := s.LeafName(); got != tc.want {
			t.Errorf("LeafName(%q) = %q, want %q", tc.subpath, got, tc.want)
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
