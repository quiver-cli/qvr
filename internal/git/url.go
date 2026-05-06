package git

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// SubdirURL describes a subdirectory inside a remote git repo, parsed from a
// platform-specific browse URL (e.g. a GitHub `/blob/<ref>/<path>` link).
type SubdirURL struct {
	RepoURL string // canonical clone URL, e.g. "https://github.com/owner/repo.git"
	Ref     string // branch, tag, or commit
	Subpath string // path inside the repo, no leading or trailing slash
}

// ErrNotSubdirURL reports that the input URL does not match any known
// subdirectory-pointing pattern. Callers can use this to fall back to
// "treat as a plain clone URL" rather than failing.
var ErrNotSubdirURL = errors.New("not a recognized subdirectory URL")

// ParseSubdirURL recognizes browse URLs that point at a subdirectory inside a
// remote repo and extracts (clone URL, ref, subpath). Currently supports
// GitHub `/blob/<ref>/<path>` and `/tree/<ref>/<path>` URLs. For other hosts
// or for refs containing slashes, callers should accept explicit --ref /
// --subdir flags instead.
//
// Returns ErrNotSubdirURL when the URL doesn't match any known pattern, so
// callers can fall back cleanly.
func ParseSubdirURL(raw string) (*SubdirURL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, ErrNotSubdirURL
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, ErrNotSubdirURL
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, ErrNotSubdirURL
	}
	host := strings.ToLower(u.Host)
	switch host {
	case "github.com", "www.github.com":
		return parseGitHubSubdir(u)
	}
	return nil, ErrNotSubdirURL
}

// parseGitHubSubdir handles the github.com `/owner/repo/(blob|tree)/<ref>/<path>` shape.
func parseGitHubSubdir(u *url.URL) (*SubdirURL, error) {
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 5 {
		return nil, ErrNotSubdirURL
	}
	owner, repo := parts[0], parts[1]
	if parts[2] != "blob" && parts[2] != "tree" {
		return nil, ErrNotSubdirURL
	}
	ref := parts[3]
	subparts := parts[4:]
	subpath := strings.Trim(strings.Join(subparts, "/"), "/")
	if owner == "" || repo == "" || ref == "" || subpath == "" {
		return nil, ErrNotSubdirURL
	}
	repo = strings.TrimSuffix(repo, ".git")
	return &SubdirURL{
		RepoURL: fmt.Sprintf("https://github.com/%s/%s.git", owner, repo),
		Ref:     ref,
		Subpath: subpath,
	}, nil
}

// LeafName returns the last non-empty segment of the subpath, suitable as a
// default skill name (e.g. "skills/jchopard69/x-article-editor" → "x-article-editor").
func (s *SubdirURL) LeafName() string {
	parts := strings.Split(strings.Trim(s.Subpath, "/"), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return ""
}

// SanitizeURL removes embedded credentials from a remote URL and reports
// whether any were present.
//
// HTTPS URLs with basic-auth userinfo (e.g. `https://user:token@host/path`)
// are stripped to `https://host/path`. SSH URLs in their two common forms
// (`ssh://git@host/path` and `git@host:path`) carry only a username, which
// is preserved — usernames alone are not secrets.
//
// The returned `clean` string is always safe to persist on disk and display
// in logs. `hadCreds` is true iff a password / token was present.
//
// Callers should use SanitizeURL on any URL received from a user before
// writing it to config or including it in error messages.
func SanitizeURL(raw string) (clean string, hadCreds bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false, nil
	}

	// scp-style SSH ("git@github.com:foo/bar.git"). No password field exists
	// in that form, so nothing to strip. Return as-is.
	if isSCPStyle(raw) {
		return raw, false, nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		return raw, false, err
	}
	if u.User == nil {
		return raw, false, nil
	}
	scheme := strings.ToLower(u.Scheme)
	_, hasPass := u.User.Password()
	switch scheme {
	case "ssh":
		// For SSH, the username is identity (e.g. `git`), not a secret.
		// Only a password field counts as credentials.
		if hasPass {
			hadCreds = true
		}
		if user := u.User.Username(); user != "" {
			u.User = url.User(user)
		} else {
			u.User = nil
		}
	default:
		// For http/https/other, any userinfo is treated as credentials —
		// GitHub accepts both `https://user:token@host` and
		// `https://token@host`, and we can't tell them apart by parsing.
		hadCreds = true
		u.User = nil
	}
	return u.String(), hadCreds, nil
}

// scpStyleRe matches "user@host:path" SSH shorthand. The path must not
// contain slashes before the colon, and the colon must not be followed by a
// digit (which would indicate a port — i.e. a real URL fragment like
// "//host:22/").
var scpStyleRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+@[a-zA-Z0-9.-]+:[^/]`)

func isSCPStyle(s string) bool {
	if strings.Contains(s, "://") {
		return false
	}
	return scpStyleRe.MatchString(s)
}

// credsInURLRe matches `://user:pass@host` where `pass` is non-empty. It is
// intentionally permissive — we'd rather redact one too many substrings in
// error messages than leak a real token.
var credsInURLRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)([^:/@\s]+):([^@\s]+)@`)

// redactCreds rewrites credentialed URLs in free-text error output so no
// token leaks into logs. We only redact the password half — leaving the
// username intact helps debugging (you can see *which* identity failed).
func redactCreds(s string) string {
	return credsInURLRe.ReplaceAllString(s, "$1$2:***@")
}
