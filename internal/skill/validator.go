package skill

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/raks097/quiver/internal/model"
)

// Severity represents the severity of a validation error.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// ValidationError represents a single validation issue.
type ValidationError struct {
	Field    string   `json:"field"`
	Message  string   `json:"message"`
	Severity Severity `json:"severity"`
}

func (v ValidationError) Error() string {
	return fmt.Sprintf("[%s] %s: %s", v.Severity, v.Field, v.Message)
}

// ValidationResult holds all validation errors for a skill.
type ValidationResult struct {
	Valid  bool              `json:"valid"`
	Path   string            `json:"path"`
	Errors []ValidationError `json:"errors"`
}

var nameRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// Validate checks a loaded skill against the agentskills.io specification.
func Validate(s *model.Skill) *ValidationResult {
	result := &ValidationResult{
		Path:   s.Dir,
		Errors: []ValidationError{},
	}

	validateName(s, result)
	validateDescription(s, result)
	validateLicense(s, result)
	validateCompatibility(s, result)
	validateAllowedTools(s, result)
	validateNameDirMatch(s, result)

	result.Valid = len(result.Errors) == 0
	return result
}

func validateName(s *model.Skill, r *ValidationResult) {
	name := s.Frontmatter.Name

	if name == "" {
		r.Errors = append(r.Errors, ValidationError{
			Field:    "name",
			Message:  "name is required",
			Severity: SeverityError,
		})
		return
	}

	if utf8.RuneCountInString(name) > 64 {
		r.Errors = append(r.Errors, ValidationError{
			Field:    "name",
			Message:  fmt.Sprintf("name must be 1-64 characters, got %d", utf8.RuneCountInString(name)),
			Severity: SeverityError,
		})
	}

	if !nameRegex.MatchString(name) {
		r.Errors = append(r.Errors, ValidationError{
			Field:    "name",
			Message:  "name must contain only lowercase alphanumeric characters and hyphens, and must not start or end with a hyphen",
			Severity: SeverityError,
		})
	}

	if strings.Contains(name, "--") {
		r.Errors = append(r.Errors, ValidationError{
			Field:    "name",
			Message:  "name must not contain consecutive hyphens",
			Severity: SeverityError,
		})
	}
}

func validateDescription(s *model.Skill, r *ValidationResult) {
	desc := s.Frontmatter.Description

	if strings.TrimSpace(desc) == "" {
		r.Errors = append(r.Errors, ValidationError{
			Field:    "description",
			Message:  "description is required and must not be empty",
			Severity: SeverityError,
		})
		return
	}

	if utf8.RuneCountInString(desc) > 1024 {
		r.Errors = append(r.Errors, ValidationError{
			Field:    "description",
			Message:  fmt.Sprintf("description must be 1-1024 characters, got %d", utf8.RuneCountInString(desc)),
			Severity: SeverityError,
		})
	}
}

func validateLicense(s *model.Skill, r *ValidationResult) {
	// License is optional; if present, it must be a non-empty string (already typed as string).
	// No additional validation per spec.
}

func validateCompatibility(s *model.Skill, r *ValidationResult) {
	compat := s.Frontmatter.Compatibility
	if compat == "" {
		return
	}
	if utf8.RuneCountInString(compat) > 500 {
		r.Errors = append(r.Errors, ValidationError{
			Field:    "compatibility",
			Message:  fmt.Sprintf("compatibility must be 1-500 characters, got %d", utf8.RuneCountInString(compat)),
			Severity: SeverityError,
		})
	}
}

func validateAllowedTools(s *model.Skill, r *ValidationResult) {
	// allowed-tools is optional; if present, it's a space-delimited string.
	// No further validation per spec (experimental field).
}

func validateNameDirMatch(s *model.Skill, r *ValidationResult) {
	if s.Frontmatter.Name == "" || s.Name == "" {
		return
	}
	if s.Frontmatter.Name != s.Name {
		r.Errors = append(r.Errors, ValidationError{
			Field:    "name",
			Message:  fmt.Sprintf("name %q must match directory name %q", s.Frontmatter.Name, s.Name),
			Severity: SeverityError,
		})
	}
}
