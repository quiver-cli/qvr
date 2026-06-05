// Package redact anonymizes secret-shaped substrings in captured trace bytes.
// It runs at capture time so redaction trickles down into everything derived
// from the raw rows (spans, exports, the UI) — the raw store never holds a live
// credential, yet the trace stays canonical: only the secret *value* is masked,
// never the surrounding reasoning, structure, or JSON validity.
//
// Patterns come from pkg/secretpatterns — the SAME source of truth the security
// scanner uses — so a credential shape only has to be defined once.
package redact

import (
	"regexp"

	"github.com/quiver-cli/qvr/pkg/secretpatterns"
)

// Marker replaces a detected secret value. It contains no JSON-structural
// characters, so substituting it inside a JSON string never breaks the JSON.
const Marker = "[REDACTED]"

// keyPrefix matches the "key<sep> " lead-in of an assignment-shape match
// (password=, api_key:, bearer , aws_secret_access_key = …) so we can keep the
// key and mask only the value — preserving the line's logic.
var keyPrefix = regexp.MustCompile(`(?i)^[A-Za-z_][\w\-]*\s*[:=]?\s*`)

// Redactor holds the compiled credential patterns, split into two families
// because they mask differently: a credential-prefix match IS the secret (mask
// whole), while an assignment-shape match is key+value (keep key, mask value).
type Redactor struct {
	prefixes    []*regexp.Regexp
	assignments []*regexp.Regexp
}

// New compiles the default credential patterns. Patterns that fail to compile
// are skipped (they are covered by pkg/secretpatterns tests, so this is
// defensive only) rather than failing capture.
func New() *Redactor {
	r := &Redactor{}
	for _, p := range secretpatterns.CredentialPrefixes() {
		if re, err := p.Compile(); err == nil {
			r.prefixes = append(r.prefixes, re)
		}
	}
	for _, p := range secretpatterns.AssignmentShapes() {
		if re, err := p.Compile(); err == nil {
			r.assignments = append(r.assignments, re)
		}
	}
	return r
}

// shared is the process-wide default redactor (compiled once).
var shared = New()

// Bytes returns a redacted copy of b using the default redactor. The input is
// not mutated.
func Bytes(b []byte) []byte { return shared.Bytes(b) }

// Bytes masks every secret it finds in a copy of b.
func (r *Redactor) Bytes(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	out := append([]byte(nil), b...)

	// Whole-match masking for high-precision credential tokens.
	for _, re := range r.prefixes {
		out = re.ReplaceAllFunc(out, func(m []byte) []byte {
			return mask(nil, m)
		})
	}
	// Value-only masking for key=value shapes (keep the key, mask the value).
	for _, re := range r.assignments {
		out = re.ReplaceAllFunc(out, func(m []byte) []byte {
			keep := 0
			if loc := keyPrefix.FindIndex(m); loc != nil {
				keep = loc[1]
			}
			return mask(m[:keep], m)
		})
	}
	return out
}

// mask builds "<keep>[REDACTED]<trailing-delims>" for a match m. Any trailing
// JSON-structural characters the greedy regex swallowed (a closing quote,
// brace, bracket, comma, or whitespace) are preserved so redacting inside a
// JSON string value can never produce invalid JSON.
func mask(keep, m []byte) []byte {
	end := len(m)
	for end > 0 && isDelim(m[end-1]) {
		end--
	}
	if end <= len(keep) {
		// Nothing but key/delimiters matched — leave it untouched.
		return m
	}
	out := make([]byte, 0, len(keep)+len(Marker)+(len(m)-end))
	out = append(out, keep...)
	out = append(out, Marker...)
	out = append(out, m[end:]...)
	return out
}

func isDelim(c byte) bool {
	switch c {
	case '"', '}', ']', ',', ' ', '\t', '\r', '\n':
		return true
	default:
		return false
	}
}
