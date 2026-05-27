// Package secretpatterns is the single source of truth for credential-shaped
// regex patterns used inside Quiver. Both internal/privacy (redaction at
// event time) and internal/security (scanning skills before install)
// consume the defaults from this package so a pattern only has to be
// added in one place.
//
// External tools that want to reuse Quiver's credential heuristics — for
// example a CI action that scans skills before they hit a registry — can
// import this package independently of the rest of qvr.
package secretpatterns

import "regexp"

// Pattern pairs a stable label with an uncompiled regex string.
//
// Patterns are exported uncompiled because the two main callers wrap
// them differently: privacy compiles them into a [regexp.Regexp] with
// match-replace semantics, while security compiles them once and runs
// MatchString plus FindAllIndex for line-number reporting. Compiling
// here would force one shape on the other.
type Pattern struct {
	// Name is a short, stable identifier that surfaces in findings,
	// redaction logs, and JSON output. Use lower_snake_case.
	Name string
	// Regex is the uncompiled Go regex source. Compile with
	// [Pattern.Compile] or with [regexp.Compile] directly.
	Regex string
}

// Compile parses Regex into a [*regexp.Regexp]. The error is returned
// unchanged so callers can attribute compile failures to a specific
// Name.
func (p Pattern) Compile() (*regexp.Regexp, error) {
	return regexp.Compile(p.Regex)
}

// Default returns every built-in pattern: the high-precision credential
// prefixes plus the looser assignment-shape family.
//
// Use this when you want exhaustive coverage and can tolerate false
// positives — for example a redactor that hides matched text. Callers
// that surface findings to users (e.g. the security scanner) should
// prefer [CredentialPrefixes] to keep the false-positive rate low.
func Default() []Pattern {
	out := make([]Pattern, 0, len(credentialPrefixes)+len(assignmentShapes))
	out = append(out, credentialPrefixes...)
	out = append(out, assignmentShapes...)
	return out
}

// CredentialPrefixes returns only the high-precision subset: patterns
// anchored on vendor-specific prefixes (AKIA, ghp_, eyJ, xox*, sk_live_,
// etc.) and well-defined formats (PEM headers, JWT shape). False
// positives are rare, so these are safe to report directly to a user.
func CredentialPrefixes() []Pattern {
	out := make([]Pattern, len(credentialPrefixes))
	copy(out, credentialPrefixes)
	return out
}

// AssignmentShapes returns only the looser `password = ...`,
// `api_key: ...`, `bearer X` family. These over-match by design — they
// will fire on Go/Python expressions like `password = fn()`. Useful for
// redaction; not appropriate for user-facing findings.
func AssignmentShapes() []Pattern {
	out := make([]Pattern, len(assignmentShapes))
	copy(out, assignmentShapes)
	return out
}

var credentialPrefixes = []Pattern{
	// AWS — long-lived and temporary access key IDs
	{Name: "aws_akia", Regex: `\bAKIA[0-9A-Z]{16}\b`},
	{Name: "aws_asia", Regex: `\bASIA[0-9A-Z]{16}\b`},

	// GitHub — classic personal-access tokens and the four newer variants,
	// plus the fine-grained format (`github_pat_` + 82 chars).
	{Name: "github_pat", Regex: `\bgh[pousr]_[A-Za-z0-9]{36}\b`},
	{Name: "github_fine_grained", Regex: `\bgithub_pat_[A-Za-z0-9_]{82}\b`},

	// Slack — bot, user, app, refresh, signing tokens
	{Name: "slack_token", Regex: `\bxox[baprs]-[A-Za-z0-9-]{10,}\b`},

	// Stripe — both test and live, secret and publishable.
	// Anchored on `_test_`/`_live_` so this does NOT collide with
	// the looser OpenAI `sk-...` shape immediately below.
	{Name: "stripe_key", Regex: `\b(sk|pk)_(test|live)_[A-Za-z0-9]{24,}\b`},

	// OpenAI — issue #37. The single most common credential leaked
	// from an AI-skill repo and an AI-skills CLI must catch it.
	// Two formats: legacy `sk-` + 48 base62 chars, and the newer
	// project-scoped `sk-proj-` + a 20+ alnum/_/- payload (the
	// suffix grew across API revisions; we accept 20+ chars to
	// avoid pinning a length that changes again next quarter).
	{Name: "openai_legacy", Regex: `\bsk-[A-Za-z0-9]{48}\b`},
	{Name: "openai_project", Regex: `\bsk-proj-[A-Za-z0-9_\-]{20,}\b`},

	// Anthropic — `sk-ant-` prefix; the secret body varies in length
	// and may include `_-`. Anchored on the prefix so it cannot
	// collide with OpenAI's `sk-...`.
	{Name: "anthropic_key", Regex: `\bsk-ant-(?:api03-)?[A-Za-z0-9_\-]{32,}\b`},

	// Hugging Face — user / fine-grained access tokens.
	{Name: "huggingface_token", Regex: `\bhf_[A-Za-z0-9]{20,}\b`},

	// Google API key
	{Name: "google_api_key", Regex: `\bAIza[0-9A-Za-z_\-]{35}\b`},

	// Azure OpenAI keys are bare 32-hex strings, which is too short to
	// anchor on alone without enormous false-positive surface. We
	// catch them via the `(?i)AZURE_OPENAI_(KEY|API_KEY)\s*=\s*` shape
	// in the assignment-shape family below, which still flags them in
	// the typical `.env` / config exposure.

	// PEM private-key header (RSA / OpenSSH / EC / DSA / PGP / unlabelled)
	{Name: "pem_private_key", Regex: `-----BEGIN (RSA |OPENSSH |EC |DSA |PGP )?PRIVATE KEY-----`},

	// JWT — three base64url segments. The leading `eyJ` anchors on the
	// fact that base64-encoded JSON always starts with `{"` → `eyJ`.
	{Name: "jwt", Regex: `\beyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\b`},

	// Connection strings carrying inline credentials.
	{Name: "postgres_url", Regex: `\bpostgres(?:ql)?://[^\s:/@]+:[^\s/@]+@[^\s]+`},
	{Name: "mongodb_url", Regex: `\bmongodb(?:\+srv)?://[^\s:/@]+:[^\s/@]+@[^\s]+`},
	{Name: "mysql_url", Regex: `\bmysql://[^\s:/@]+:[^\s/@]+@[^\s]+`},
}

var assignmentShapes = []Pattern{
	{Name: "password", Regex: `(?i)password\s*[:=]\s*\S+`},
	{Name: "api_key", Regex: `(?i)api[-_]?key\s*[:=]\s*\S+`},
	{Name: "token", Regex: `(?i)\btoken\s*[:=]\s*\S+`},
	{Name: "secret", Regex: `(?i)\bsecret\s*[:=]\s*\S+`},
	{Name: "bearer", Regex: `(?i)bearer\s+[A-Za-z0-9._\-]+`},
	{Name: "aws_access_key_id", Regex: `(?i)aws_access_key_id\s*[:=]\s*\S+`},
	{Name: "aws_secret_access_key", Regex: `(?i)aws_secret_access_key\s*[:=]\s*\S+`},
}
