package git

import (
	"strings"
	"testing"
)

func TestParseGitVersion(t *testing.T) {
	cases := []struct {
		in         string
		major, min int
		ok         bool
	}{
		{"git version 2.39.3", 2, 39, true},
		{"git version 2.30.1 (Apple Git-130)", 2, 30, true},
		{"git version 2.43.0.windows.1", 2, 43, true},
		{"git version 3.0.0", 3, 0, true},
		{"git version 1.9.5", 1, 9, true},
		{"", 0, 0, false},
		{"not a version string", 0, 0, false},
		{"git version x.y.z", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			maj, min, ok := parseGitVersion(tc.in)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !tc.ok {
				return
			}
			if maj != tc.major || min != tc.min {
				t.Errorf("got %d.%d, want %d.%d", maj, min, tc.major, tc.min)
			}
		})
	}
}

func TestScrubbedEnv(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"HOME=/home/user",
		"GIT_DIR=/poisoned/dir",
		"GIT_WORK_TREE=/poisoned/wt",
		"GIT_INDEX_FILE=/poisoned/index",
		"GIT_COMMON_DIR=/poisoned/common",
		"GIT_NAMESPACE=poisoned",
		"GIT_OBJECT_DIRECTORY=/poisoned/obj",
		"SSH_AUTH_SOCK=/tmp/ssh-agent.sock",
		"KEEPME=yes",
	}
	out := scrubbedEnv(in)
	joined := strings.Join(out, "\n")

	mustContain := []string{
		"PATH=/usr/bin",
		"HOME=/home/user",
		"SSH_AUTH_SOCK=/tmp/ssh-agent.sock",
		"KEEPME=yes",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/true",
	}
	for _, s := range mustContain {
		if !strings.Contains(joined, s) {
			t.Errorf("missing %q in scrubbed env:\n%s", s, joined)
		}
	}

	mustNotContain := []string{
		"GIT_DIR=",
		"GIT_WORK_TREE=",
		"GIT_INDEX_FILE=",
		"GIT_COMMON_DIR=",
		"GIT_NAMESPACE=",
		"GIT_OBJECT_DIRECTORY=",
	}
	for _, s := range mustNotContain {
		if strings.Contains(joined, s) {
			t.Errorf("scrubbed env still contains %q:\n%s", s, joined)
		}
	}
}

func TestScrubbedEnv_PreservesCallerOverrides(t *testing.T) {
	// If the caller (or CI) already set GIT_TERMINAL_PROMPT, honour it by
	// forcing to 0 anyway — we never want an interactive prompt.
	in := []string{"GIT_TERMINAL_PROMPT=1"}
	out := scrubbedEnv(in)
	found := false
	for _, e := range out {
		if e == "GIT_TERMINAL_PROMPT=0" {
			found = true
		}
		if e == "GIT_TERMINAL_PROMPT=1" {
			t.Errorf("scrubbed env should force GIT_TERMINAL_PROMPT=0, got %q", e)
		}
	}
	if !found {
		t.Errorf("GIT_TERMINAL_PROMPT=0 not in scrubbed env: %v", out)
	}
}

func TestScrubbedEnv_KeepsExistingAskpass(t *testing.T) {
	// Caller-provided GIT_ASKPASS should be preserved (user-configured helper).
	in := []string{"GIT_ASKPASS=/usr/local/bin/my-askpass"}
	out := scrubbedEnv(in)
	found := false
	for _, e := range out {
		if e == "GIT_ASKPASS=/usr/local/bin/my-askpass" {
			found = true
		}
	}
	if !found {
		t.Errorf("caller's GIT_ASKPASS was not preserved: %v", out)
	}
}

func TestParseLsRemote(t *testing.T) {
	in := "abc123def456abc123def456abc123def456abcd\tHEAD\n" +
		"abc123def456abc123def456abc123def456abcd\trefs/heads/main\n" +
		"1111111111111111111111111111111111111111\trefs/tags/v1.0.0\n" +
		"2222222222222222222222222222222222222222\trefs/tags/v1.0.0^{}\n"
	refs, err := parseLsRemote(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parseLsRemote: %v", err)
	}
	if refs.Refs["HEAD"] != "abc123def456abc123def456abc123def456abcd" {
		t.Errorf("HEAD = %q", refs.Refs["HEAD"])
	}
	if refs.Refs["refs/heads/main"] != "abc123def456abc123def456abc123def456abcd" {
		t.Errorf("main = %q", refs.Refs["refs/heads/main"])
	}
	// Peeled entry should have overwritten the tag-object hash with the
	// commit hash it points to.
	if refs.Refs["refs/tags/v1.0.0"] != "2222222222222222222222222222222222222222" {
		t.Errorf("v1.0.0 peeled = %q, want commit hash", refs.Refs["refs/tags/v1.0.0"])
	}
}

func TestParseLsRemote_Empty(t *testing.T) {
	refs, err := parseLsRemote(strings.NewReader(""))
	if err != nil {
		t.Fatalf("parseLsRemote: %v", err)
	}
	if len(refs.Refs) != 0 {
		t.Errorf("expected empty, got %v", refs.Refs)
	}
}

func TestRedactCreds(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{
			in:   "fatal: could not read from https://user:token@github.com/foo/bar.git",
			want: "fatal: could not read from https://user:***@github.com/foo/bar.git",
		},
		{
			in:   "remote: Permission denied on https://x-access-token:ghp_abc@github.com/org/repo",
			want: "remote: Permission denied on https://x-access-token:***@github.com/org/repo",
		},
		{
			in:   "no creds here, just https://github.com/foo/bar.git",
			want: "no creds here, just https://github.com/foo/bar.git",
		},
		{
			in:   "ssh://git:deadbeef@example.com/repo",
			want: "ssh://git:***@example.com/repo",
		},
	}
	for _, tc := range cases {
		got := redactCreds(tc.in)
		if got != tc.want {
			t.Errorf("redactCreds(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestClassifyNetworkErr(t *testing.T) {
	cases := []struct {
		msg      string
		fallback error
		want     error
	}{
		{"fatal: repository 'https://github.com/x/y.git' not found", ErrCloneFailed, ErrRepoNotFound},
		{"fatal: Could not read from remote repository", ErrCloneFailed, ErrRepoNotFound},
		{"fatal: Authentication failed for 'https://github.com/x/y.git'", ErrCloneFailed, ErrCloneFailed},
		{"fatal: terminal prompts disabled", ErrFetchFailed, ErrFetchFailed},
		{"fatal: Permission denied (publickey)", ErrPushFailed, ErrPushFailed},
		{"some other error", ErrCloneFailed, ErrCloneFailed},
	}
	for _, tc := range cases {
		err := classifyNetworkErr(simpleErr(tc.msg), tc.fallback)
		if err == nil {
			t.Fatalf("got nil error for %q", tc.msg)
		}
		// Must wrap the expected sentinel so callers can errors.Is it.
		if !strings.Contains(err.Error(), tc.want.Error()) {
			t.Errorf("err %q does not mention sentinel %q", err, tc.want)
		}
	}
}

type simpleErr string

func (s simpleErr) Error() string { return string(s) }
