package skill_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/skill"
	"github.com/astra-sh/qvr/pkg/skillspec"
)

func TestLint_ValidSkill(t *testing.T) {
	s := makeSkill("my-skill", "A valid description of this skill.", "my-skill")
	result := skill.Lint(s)
	if !result.Valid {
		t.Errorf("expected valid, got errors: %v", result.Errors)
	}
}

func TestLint_MissingName(t *testing.T) {
	s := makeSkill("", "A description.", "some-dir")
	result := skill.Lint(s)
	if result.Valid {
		t.Error("expected invalid for missing name")
	}
	assertHasError(t, result, "name", "required")
}

func TestLint_NameTooLong(t *testing.T) {
	longName := strings.Repeat("a", 65)
	s := makeSkill(longName, "Desc.", longName)
	result := skill.Lint(s)
	if result.Valid {
		t.Error("expected invalid for long name")
	}
	assertHasError(t, result, "name", "1-64")
}

func TestLint_NameUppercase(t *testing.T) {
	s := makeSkill("Bad-Name", "Desc.", "Bad-Name")
	result := skill.Lint(s)
	if result.Valid {
		t.Error("expected invalid for uppercase name")
	}
	assertHasError(t, result, "name", "lowercase")
}

func TestLint_NameStartsWithHyphen(t *testing.T) {
	s := makeSkill("-bad", "Desc.", "-bad")
	result := skill.Lint(s)
	if result.Valid {
		t.Error("expected invalid for name starting with hyphen")
	}
}

func TestLint_NameEndsWithHyphen(t *testing.T) {
	s := makeSkill("bad-", "Desc.", "bad-")
	result := skill.Lint(s)
	if result.Valid {
		t.Error("expected invalid for name ending with hyphen")
	}
}

func TestLint_NameConsecutiveHyphens(t *testing.T) {
	s := makeSkill("bad--name", "Desc.", "bad--name")
	result := skill.Lint(s)
	if result.Valid {
		t.Error("expected invalid for consecutive hyphens")
	}
	assertHasError(t, result, "name", "consecutive")
}

// TestLint_NameRejectsTraversalAndSlash pins the adversarial cases the
// loader must catch before a malicious frontmatter name turns into a
// lockfile-write that escapes the agent skills directory. The regex
// `^[a-z0-9]([a-z0-9-]*[a-z0-9])?$` rejects `/`, `..`, null bytes, leading
// dots, etc. — this test is the spec, not the implementation.
func TestLint_NameRejectsTraversalAndSlash(t *testing.T) {
	hostile := []string{
		"../escape",
		"..",
		"foo/bar",
		"foo\x00bar",
		".hidden",
		" leading-space",
	}
	for _, name := range hostile {
		t.Run(name, func(t *testing.T) {
			s := makeSkill(name, "Desc.", name)
			result := skill.Lint(s)
			if result.Valid {
				t.Errorf("hostile name %q should be invalid, got valid", name)
			}
		})
	}
}

func TestLint_NameSingleChar(t *testing.T) {
	s := makeSkill("a", "Desc.", "a")
	result := skill.Lint(s)
	if !result.Valid {
		t.Errorf("single char name should be valid, got errors: %v", result.Errors)
	}
}

func TestLint_NameWithNumbers(t *testing.T) {
	s := makeSkill("skill-v2", "Desc.", "skill-v2")
	result := skill.Lint(s)
	if !result.Valid {
		t.Errorf("name with numbers should be valid, got errors: %v", result.Errors)
	}
}

func TestLint_EmptyDescription(t *testing.T) {
	s := makeSkill("test", "", "test")
	result := skill.Lint(s)
	if result.Valid {
		t.Error("expected invalid for empty description")
	}
	assertHasError(t, result, "description", "required")
}

func TestLint_WhitespaceDescription(t *testing.T) {
	s := makeSkill("test", "   \n\t  ", "test")
	result := skill.Lint(s)
	if result.Valid {
		t.Error("expected invalid for whitespace-only description")
	}
}

func TestLint_DescriptionTooLong(t *testing.T) {
	longDesc := strings.Repeat("x", 1025)
	s := makeSkill("test", longDesc, "test")
	result := skill.Lint(s)
	if result.Valid {
		t.Error("expected invalid for long description")
	}
	assertHasError(t, result, "description", "1-1024")
}

func TestLint_NameDirMismatch(t *testing.T) {
	s := makeSkill("skill-a", "Desc.", "skill-b")
	result := skill.Lint(s)
	if result.Valid {
		t.Error("expected invalid for name/dir mismatch")
	}
	assertHasError(t, result, "name", "match directory")
}

func TestLint_CompatibilityTooLong(t *testing.T) {
	s := &model.Skill{
		Skill: skillspec.Skill{
			Frontmatter: skillspec.Frontmatter{
				Name:          "test",
				Description:   "Desc.",
				Compatibility: strings.Repeat("x", 501),
			},
		},
		Dir:  "/test/test",
		Name: "test",
	}
	result := skill.Lint(s)
	if result.Valid {
		t.Error("expected invalid for long compatibility")
	}
	assertHasError(t, result, "compatibility", "1-500")
}

func TestLint_ValidCompatibility(t *testing.T) {
	s := &model.Skill{
		Skill: skillspec.Skill{
			Frontmatter: skillspec.Frontmatter{
				Name:          "test",
				Description:   "Desc.",
				Compatibility: "Requires git and docker",
			},
		},
		Dir:  "/test/test",
		Name: "test",
	}
	result := skill.Lint(s)
	if !result.Valid {
		t.Errorf("valid compatibility should pass, got: %v", result.Errors)
	}
}

// Integration test: load from testdata and validate
func TestLint_TestdataValidSkill(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "valid-skill")
	s, err := skill.LoadFromPath(dir)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	result := skill.Lint(s)
	if !result.Valid {
		t.Errorf("valid-skill testdata should pass validation, got: %v", result.Errors)
	}
}

func TestLint_TestdataNoName(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "invalid-skill-no-name")
	s, err := skill.LoadFromPath(dir)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	result := skill.Lint(s)
	if result.Valid {
		t.Error("invalid-skill-no-name should fail")
	}
	assertHasError(t, result, "name", "required")
}

func TestLint_TestdataNameMismatch(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "invalid-skill-name-mismatch")
	s, err := skill.LoadFromPath(dir)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	result := skill.Lint(s)
	if result.Valid {
		t.Error("invalid-skill-name-mismatch should fail")
	}
	assertHasError(t, result, "name", "match directory")
}

func TestLint_TestdataEmptyDesc(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "invalid-skill-empty-desc")
	s, err := skill.LoadFromPath(dir)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	result := skill.Lint(s)
	if result.Valid {
		t.Error("empty-desc should fail")
	}
}

func TestLint_TestdataConsecutiveHyphens(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "invalid-skill-consecutive-hyphens")
	s, err := skill.LoadFromPath(dir)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	result := skill.Lint(s)
	if result.Valid {
		t.Error("consecutive-hyphens should fail")
	}
}

func TestLint_TestdataStartsHyphen(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "invalid-skill-starts-hyphen")
	s, err := skill.LoadFromPath(dir)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	result := skill.Lint(s)
	if result.Valid {
		t.Error("starts-hyphen should fail")
	}
}

func TestLint_TestdataUppercase(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "invalid-skill-uppercase")
	s, err := skill.LoadFromPath(dir)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	result := skill.Lint(s)
	if result.Valid {
		t.Error("uppercase should fail")
	}
}

// Multiple errors at once
func TestLint_MultipleErrors(t *testing.T) {
	s := makeSkill("", "", "some-dir")
	result := skill.Lint(s)
	if result.Valid {
		t.Error("expected invalid")
	}
	if len(result.Errors) < 2 {
		t.Errorf("expected at least 2 errors, got %d", len(result.Errors))
	}
}
