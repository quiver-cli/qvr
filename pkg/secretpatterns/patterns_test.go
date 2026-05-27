package secretpatterns_test

import (
	"regexp"
	"testing"

	"github.com/raks097/quiver/pkg/secretpatterns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAllPatternsCompile(t *testing.T) {
	for _, p := range secretpatterns.Default() {
		t.Run(p.Name, func(t *testing.T) {
			_, err := p.Compile()
			require.NoErrorf(t, err, "pattern %q failed to compile", p.Name)
		})
	}
}

func TestNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range secretpatterns.Default() {
		assert.Falsef(t, seen[p.Name], "duplicate pattern name %q", p.Name)
		seen[p.Name] = true
	}
}

func TestDefaultSplit(t *testing.T) {
	// Default() must equal CredentialPrefixes() ++ AssignmentShapes()
	// in that order — privacy depends on this concatenation to keep its
	// historical match order stable.
	pre := secretpatterns.CredentialPrefixes()
	post := secretpatterns.AssignmentShapes()
	merged := append(append([]secretpatterns.Pattern{}, pre...), post...)
	assert.Equal(t, merged, secretpatterns.Default())
}

func TestSlicesAreCopies(t *testing.T) {
	// Mutating returned slices must not affect subsequent calls.
	a := secretpatterns.Default()
	if len(a) > 0 {
		a[0].Name = "mutated"
	}
	b := secretpatterns.Default()
	assert.NotEqual(t, "mutated", b[0].Name)
}

// Each pattern needs a positive match and a representative negative case.
// Keep the case material short and explicit — the privacy package owns
// the comprehensive fixture file; this table is for fast feedback on the
// regex source.
type patternCase struct {
	name     string
	positive []string
	negative []string
}

func TestCredentialPrefixMatches(t *testing.T) {
	cases := []patternCase{
		{
			name:     "aws_akia",
			positive: []string{"AKIAIOSFODNN7EXAMPLE", "key=AKIAZZZZZZZZZZZZZZZZ here"},
			negative: []string{"AKIAshort", "AKIB1234567890ABCDEF0", "akia1234567890abcdef0"},
		},
		{
			name:     "aws_asia",
			positive: []string{"ASIAIOSFODNN7EXAMPLE"},
			negative: []string{"AKIAIOSFODNN7EXAMPLE", "order-ASIA-ticket"},
		},
		{
			name:     "github_pat",
			positive: []string{"ghp_abcdef1234567890ABCDEFGHIJKLMNOPqrst", "gho_abcdef1234567890ABCDEFGHIJKLMNOPqrst"},
			negative: []string{"ghp_short", "ghx_abcdef1234567890ABCDEFGHIJKLMNOPqrst"},
		},
		{
			name:     "github_fine_grained",
			positive: []string{"github_pat_11AAAAAAAA0123456789abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"},
			negative: []string{"github_pat_short", "github_pat_" + repeat("A", 81)},
		},
		{
			name:     "slack_token",
			positive: []string{"xoxb-1234567890-abcdefghij", "xoxp-12345-abcdefghij"},
			negative: []string{"xoxo-chocolate", "xoxq-not-a-token", "xoxb-9"},
		},
		{
			name:     "stripe_key",
			positive: []string{"sk_live_" + repeat("A", 24), "pk_test_" + repeat("z", 30)},
			negative: []string{"sk_live_short", "sk_other_" + repeat("A", 24)},
		},
		{
			name: "openai_legacy",
			positive: []string{
				"OPENAI_API_KEY=sk-" + repeat("A", 48),
				"key=sk-" + repeat("z", 48),
			},
			negative: []string{
				"sk-short",
				"sk_live_" + repeat("A", 48), // must not collide with stripe
				"sk-proj-" + repeat("A", 48), // project keys belong to the other rule
			},
		},
		{
			name: "openai_project",
			positive: []string{
				"sk-proj-" + repeat("A", 20),
				"sk-proj-" + repeat("a", 80) + "_-Az9",
			},
			negative: []string{
				"sk-proj-short",
				"sk-" + repeat("A", 48), // legacy form
			},
		},
		{
			name: "anthropic_key",
			positive: []string{
				"sk-ant-api03-" + repeat("A", 95),
				"sk-ant-" + repeat("a", 50),
			},
			negative: []string{
				"sk-ant-short",
				"sk-" + repeat("A", 48), // OpenAI legacy
			},
		},
		{
			name: "huggingface_token",
			positive: []string{
				"HF_TOKEN=hf_" + repeat("A", 30),
				"hf_" + repeat("z", 25),
			},
			negative: []string{
				"hf_short",
				"thf_abcdefghij1234567890", // missing word boundary
			},
		},
		{
			name:     "google_api_key",
			positive: []string{"AIza" + repeat("a", 35)},
			negative: []string{"AIzashort", "AIxa" + repeat("a", 35)},
		},
		{
			name:     "pem_private_key",
			positive: []string{"-----BEGIN PRIVATE KEY-----", "-----BEGIN RSA PRIVATE KEY-----", "-----BEGIN OPENSSH PRIVATE KEY-----"},
			negative: []string{"-----BEGIN CERTIFICATE-----", "PRIVATE KEY file is missing"},
		},
		{
			name:     "jwt",
			positive: []string{"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"},
			negative: []string{"eyJsomething", "eyJsomething.eyJelse", "notevenbase64.something.else"},
		},
		{
			name:     "postgres_url",
			positive: []string{"postgres://user:pass@db.example.com:5432/db", "postgresql://u:p@host/db"},
			negative: []string{"postgres://localhost/db", "postgresql://host"},
		},
		{
			name:     "mongodb_url",
			positive: []string{"mongodb://user:pass@host:27017/db", "mongodb+srv://u:p@cluster.example.net/db"},
			negative: []string{"mongodb://localhost/db", "mongo://user:pass@host/db"},
		},
		{
			name:     "mysql_url",
			positive: []string{"mysql://user:pass@host:3306/db"},
			negative: []string{"mysql://localhost/db"},
		},
	}
	runPatternCases(t, secretpatterns.CredentialPrefixes(), cases)
}

func TestAssignmentShapeMatches(t *testing.T) {
	cases := []patternCase{
		{
			name:     "password",
			positive: []string{"password=hunter2", "Password: abc_def_ghi", "my_password = value123"},
			negative: []string{"password =", "passwordless authentication"},
		},
		{
			name:     "api_key",
			positive: []string{"api_key=abc123xyz", "API-KEY: 1234abcd", "apikey=foo-bar"},
			negative: []string{"api_key=", "api key was rotated"},
		},
		{
			name:     "token",
			positive: []string{"token=eyJhbGciOiJIUzI1NiJ9", "TOKEN: xyzvalueabc"},
			negative: []string{"token:", "tokenize the input"},
		},
		{
			name:     "secret",
			positive: []string{"secret=topsecret", "SECRET: abcdef"},
			negative: []string{"secret:", "secretary filed paperwork"},
		},
		{
			name:     "bearer",
			positive: []string{"Authorization: Bearer abc.def.ghi", "bearer XyZ-_.123"},
			negative: []string{"bearer", "unbearable", "please bear with us"},
		},
		{
			name:     "aws_access_key_id",
			positive: []string{"aws_access_key_id=AKIAIOSFODNN7EXAMPLE", "AWS_ACCESS_KEY_ID: AKIAZZZZZZZZZZZZZZZZ"},
			negative: []string{"aws_access_key_id="},
		},
		{
			name:     "aws_secret_access_key",
			positive: []string{"aws_secret_access_key=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
			negative: []string{"aws_secret_access_key="},
		},
	}
	runPatternCases(t, secretpatterns.AssignmentShapes(), cases)
}

func runPatternCases(t *testing.T, patterns []secretpatterns.Pattern, cases []patternCase) {
	t.Helper()
	byName := map[string]*regexp.Regexp{}
	for _, p := range patterns {
		r, err := p.Compile()
		require.NoErrorf(t, err, "pattern %q failed to compile", p.Name)
		byName[p.Name] = r
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := byName[c.name]
			require.NotNilf(t, r, "pattern %q not present in set", c.name)
			for _, pos := range c.positive {
				assert.Truef(t, r.MatchString(pos), "expected positive match for %q on input %q", c.name, pos)
			}
			for _, neg := range c.negative {
				assert.Falsef(t, r.MatchString(neg), "unexpected positive match for %q on input %q", c.name, neg)
			}
		})
		delete(byName, c.name)
	}
	for name := range byName {
		t.Errorf("pattern %q has no test case — add positive + negative coverage", name)
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
