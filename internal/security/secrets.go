package security

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/pkg/secretpatterns"
)

// SecretsCheckName is the [Check.Name] of [secretsCheck]. Exported so
// downstream tooling can filter findings by check name without
// importing the unexported struct.
const SecretsCheckName = "secrets"

type secretsCheck struct {
	patterns []compiledPattern
}

type compiledPattern struct {
	name string
	re   *regexp.Regexp
}

// NewSecretsCheck returns a check that scans every text file in the
// skill for high-precision credential patterns (AWS keys, GitHub PATs,
// JWTs, PEM private keys, etc.) sourced from pkg/secretpatterns.
//
// The loose `password = X` family is intentionally excluded — it has a
// documented false-positive rate that's acceptable for redaction but
// not for findings shown to a user about to install a skill.
func NewSecretsCheck() Check {
	src := secretpatterns.CredentialPrefixes()
	compiled := make([]compiledPattern, 0, len(src))
	for _, p := range src {
		r, err := p.Compile()
		if err != nil {
			// Patterns are compile-tested in pkg/secretpatterns; a
			// failure here is a programmer error worth shouting about.
			panic(fmt.Errorf("secretpatterns %q failed to compile: %w", p.Name, err))
		}
		compiled = append(compiled, compiledPattern{name: p.Name, re: r})
	}
	return &secretsCheck{patterns: compiled}
}

func (c *secretsCheck) Name() string { return SecretsCheckName }

func (c *secretsCheck) Run(_ context.Context, _ *model.Skill, files []FileEntry) []Finding {
	var findings []Finding
	for _, f := range files {
		if f.Content == "" {
			continue
		}
		lines := strings.Split(f.Content, "\n")
		for lineIdx, line := range lines {
			for _, p := range c.patterns {
				if !p.re.MatchString(line) {
					continue
				}
				findings = append(findings, Finding{
					Check:       SecretsCheckName,
					Severity:    SeverityCritical,
					File:        f.Path,
					Line:        lineIdx + 1,
					Message:     fmt.Sprintf("possible %s detected", humanise(p.name)),
					Remediation: "remove the value from the file and rotate the credential",
				})
			}
		}
	}
	return findings
}

// humanise turns a pattern name like "aws_akia" into "AWS access key"
// for the finding message. The mapping is conservative — anything not
// listed falls back to the raw name.
func humanise(name string) string {
	switch name {
	case "aws_akia", "aws_asia":
		return "AWS access key"
	case "github_pat":
		return "GitHub personal access token"
	case "github_fine_grained":
		return "GitHub fine-grained PAT"
	case "slack_token":
		return "Slack token"
	case "stripe_key":
		return "Stripe API key"
	case "google_api_key":
		return "Google API key"
	case "pem_private_key":
		return "PEM private key"
	case "jwt":
		return "JWT"
	case "postgres_url", "mongodb_url", "mysql_url":
		return "database connection string with embedded credentials"
	}
	return name
}
