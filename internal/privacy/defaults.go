package privacy

// NamedPattern pairs a label (used for MatchedRules observability) with
// the regex source. Kept as a struct so the label survives through the
// Decision surface for tests and log lines.
type NamedPattern struct {
	Label string
	Regex string
}

// DefaultSensitivePatterns returns the built-in list of filesystem
// globs that mark a path as sensitive. Patterns use doublestar syntax
// (** matches any number of path segments).
//
// Merging rule: user-supplied patterns are appended to this list;
// defaults cannot be subtracted. This is a deliberate safety floor —
// the regex checker can have false positives, but the path checker
// should never have false negatives on the well-known-dangerous paths.
func DefaultSensitivePatterns() []string {
	return []string{
		// Dotenv variants
		"**/.env",
		"**/.env.*",
		"**/.env.local",

		// Secrets directories
		"**/secrets/**",

		// Private-key extensions
		"**/*.pem",
		"**/*.key",
		"**/*.p12",
		"**/*.pfx",

		// Name-carries-meaning (filename contains the word)
		"**/*password*",
		"**/*secret*",
		"**/*credential*",

		// Git internals (config holds credential helpers)
		"**/.git/config",

		// SSH
		"**/.ssh/**",
		"**/id_rsa",
		"**/id_ed25519",
		"**/id_ecdsa",
		"**/id_dsa",

		// AWS / cloud creds
		"**/.aws/**",

		// Language registries
		"**/.npmrc",
		"**/.pypirc",

		// Kubernetes
		"**/kubeconfig",
		"**/.kube/config",

		// Legacy shell auth
		"**/.netrc",
	}
}

// DefaultRedactPatterns returns the built-in list of regex patterns
// that target secret-shaped strings embedded in otherwise-non-sensitive
// content (e.g., an AWS key hardcoded into deploy.sh).
//
// Known false-positive shape: the generic
//
//	password\s*[:=]\s*\S+
//
// family will fire on Go/Python expressions like `password = fn()`.
// This is a known limitation; fixing it would require AST awareness we
// don't want in regex-land.
func DefaultRedactPatterns() []NamedPattern {
	return []NamedPattern{
		// Generic assignment family
		{Label: "password", Regex: `(?i)password\s*[:=]\s*\S+`},
		{Label: "api_key", Regex: `(?i)api[-_]?key\s*[:=]\s*\S+`},
		{Label: "token", Regex: `(?i)\btoken\s*[:=]\s*\S+`},
		{Label: "secret", Regex: `(?i)\bsecret\s*[:=]\s*\S+`},
		{Label: "bearer", Regex: `(?i)bearer\s+[A-Za-z0-9._\-]+`},

		// AWS
		{Label: "aws_akia", Regex: `\bAKIA[0-9A-Z]{16}\b`},
		{Label: "aws_asia", Regex: `\bASIA[0-9A-Z]{16}\b`},
		{Label: "aws_key_id", Regex: `(?i)aws_access_key_id\s*[:=]\s*\S+`},
		{Label: "aws_secret", Regex: `(?i)aws_secret_access_key\s*[:=]\s*\S+`},

		// GitHub
		{Label: "github_pat", Regex: `\bghp_[A-Za-z0-9]{36}\b`},
		{Label: "github_fine_grained", Regex: `\bgithub_pat_[A-Za-z0-9_]{82}\b`},

		// Slack
		{Label: "slack_token", Regex: `\bxox[baprs]-[A-Za-z0-9-]{10,}\b`},

		// PEM private-key header (any flavour)
		{Label: "pem_block", Regex: `-----BEGIN (RSA |OPENSSH |EC |DSA |PGP )?PRIVATE KEY-----`},

		// JWT (three base64url segments)
		{Label: "jwt", Regex: `\beyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\b`},
	}
}
