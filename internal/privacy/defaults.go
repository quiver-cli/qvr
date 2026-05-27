package privacy

import "github.com/raks097/quiver/pkg/secretpatterns"

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
// The regex source lives in [secretpatterns] so the privacy redactor
// and the security scanner share one truth. Privacy uses every pattern
// (including the looser assignment-shape family) because over-redaction
// is safer than under-redaction here.
//
// Known false-positive shape: the generic
//
//	password\s*[:=]\s*\S+
//
// family will fire on Go/Python expressions like `password = fn()`.
// This is a known limitation; fixing it would require AST awareness we
// don't want in regex-land.
func DefaultRedactPatterns() []NamedPattern {
	src := secretpatterns.Default()
	out := make([]NamedPattern, len(src))
	for i, p := range src {
		out[i] = NamedPattern{Label: p.Name, Regex: p.Regex}
	}
	return out
}
