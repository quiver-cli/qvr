package skill

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/astra-sh/qvr/internal/model"
)

// Severity represents the severity of a lint issue.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// LintError represents a single lint issue.
type LintError struct {
	Field    string   `json:"field"`
	Message  string   `json:"message"`
	Severity Severity `json:"severity"`
}

func (v LintError) Error() string {
	return fmt.Sprintf("[%s] %s: %s", v.Severity, v.Field, v.Message)
}

// LintResult holds all lint issues for a skill.
type LintResult struct {
	Valid  bool        `json:"valid"`
	Path   string      `json:"path"`
	Errors []LintError `json:"errors"`
}

var nameRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// Lint checks a loaded skill against the agentskills.io specification.
//
// Lint is advisory: callers surface its result (via `qvr lint`, `qvr scan`,
// and the dashboard) but installs proceed regardless. The one exception is
// `qvr publish`, which gates on Lint so non-conformant skills aren't pushed
// into a shared registry.
func Lint(s *model.Skill) *LintResult {
	result := &LintResult{
		Path:   s.Dir,
		Errors: []LintError{},
	}

	lintName(s, result)
	lintDescription(s, result)
	lintLicense(s, result)
	lintCompatibility(s, result)
	lintAllowedTools(s, result)
	lintNameDirMatch(s, result)

	result.Valid = len(result.Errors) == 0
	return result
}

func lintName(s *model.Skill, r *LintResult) {
	name := s.Frontmatter.Name

	if name == "" {
		r.Errors = append(r.Errors, LintError{
			Field:    "name",
			Message:  "name is required",
			Severity: SeverityError,
		})
		return
	}

	if utf8.RuneCountInString(name) > 64 {
		r.Errors = append(r.Errors, LintError{
			Field:    "name",
			Message:  fmt.Sprintf("name must be 1-64 characters, got %d", utf8.RuneCountInString(name)),
			Severity: SeverityError,
		})
	}

	if !nameRegex.MatchString(name) {
		r.Errors = append(r.Errors, LintError{
			Field:    "name",
			Message:  "name must contain only lowercase alphanumeric characters and hyphens, and must not start or end with a hyphen",
			Severity: SeverityError,
		})
	}

	if strings.Contains(name, "--") {
		r.Errors = append(r.Errors, LintError{
			Field:    "name",
			Message:  "name must not contain consecutive hyphens",
			Severity: SeverityError,
		})
	}
}

func lintDescription(s *model.Skill, r *LintResult) {
	desc := s.Frontmatter.Description

	if strings.TrimSpace(desc) == "" {
		r.Errors = append(r.Errors, LintError{
			Field:    "description",
			Message:  "description is required and must not be empty",
			Severity: SeverityError,
		})
		return
	}

	if utf8.RuneCountInString(desc) > 1024 {
		r.Errors = append(r.Errors, LintError{
			Field:    "description",
			Message:  fmt.Sprintf("description must be 1-1024 characters, got %d", utf8.RuneCountInString(desc)),
			Severity: SeverityError,
		})
	}
}

func lintLicense(s *model.Skill, r *LintResult) {
	// License is optional; if present, it must be a non-empty string (already typed as string).
	// No additional lint per spec.
}

func lintCompatibility(s *model.Skill, r *LintResult) {
	compat := s.Frontmatter.Compatibility
	if compat == "" {
		return
	}
	if utf8.RuneCountInString(compat) > 500 {
		r.Errors = append(r.Errors, LintError{
			Field:    "compatibility",
			Message:  fmt.Sprintf("compatibility must be 1-500 characters, got %d", utf8.RuneCountInString(compat)),
			Severity: SeverityError,
		})
	}
}

func lintAllowedTools(s *model.Skill, r *LintResult) {
	// allowed-tools is optional; if present, it's a space-delimited string.
	// No further lint per spec (experimental field).
}

func lintNameDirMatch(s *model.Skill, r *LintResult) {
	if s.Frontmatter.Name == "" || s.Name == "" {
		return
	}
	if s.Frontmatter.Name != s.Name {
		r.Errors = append(r.Errors, LintError{
			Field:    "name",
			Message:  fmt.Sprintf("name %q must match directory name %q", s.Frontmatter.Name, s.Name),
			Severity: SeverityError,
		})
	}
}
