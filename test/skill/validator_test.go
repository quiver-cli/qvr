package skilltests

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/skill"
	"github.com/raks097/quiver/pkg/skillspec"
)

func TestValidate_ValidSkill(t *testing.T) {
	s := makeSkill("my-skill", "A valid description of this skill.", "my-skill")
	result := skill.Validate(s)
	if !result.Valid {
		t.Errorf("expected valid, got errors: %v", result.Errors)
	}
}

func TestValidate_MissingName(t *testing.T) {
	s := makeSkill("", "A description.", "some-dir")
	result := skill.Validate(s)
	if result.Valid {
		t.Error("expected invalid for missing name")
	}
	assertHasError(t, result, "name", "required")
}

func TestValidate_NameTooLong(t *testing.T) {
	longName := strings.Repeat("a", 65)
	s := makeSkill(longName, "Desc.", longName)
	result := skill.Validate(s)
	if result.Valid {
		t.Error("expected invalid for long name")
	}
	assertHasError(t, result, "name", "1-64")
}

func TestValidate_NameUppercase(t *testing.T) {
	s := makeSkill("Bad-Name", "Desc.", "Bad-Name")
	result := skill.Validate(s)
	if result.Valid {
		t.Error("expected invalid for uppercase name")
	}
	assertHasError(t, result, "name", "lowercase")
}

func TestValidate_NameStartsWithHyphen(t *testing.T) {
	s := makeSkill("-bad", "Desc.", "-bad")
	result := skill.Validate(s)
	if result.Valid {
		t.Error("expected invalid for name starting with hyphen")
	}
}

func TestValidate_NameEndsWithHyphen(t *testing.T) {
	s := makeSkill("bad-", "Desc.", "bad-")
	result := skill.Validate(s)
	if result.Valid {
		t.Error("expected invalid for name ending with hyphen")
	}
}

func TestValidate_NameConsecutiveHyphens(t *testing.T) {
	s := makeSkill("bad--name", "Desc.", "bad--name")
	result := skill.Validate(s)
	if result.Valid {
		t.Error("expected invalid for consecutive hyphens")
	}
	assertHasError(t, result, "name", "consecutive")
}

func TestValidate_NameSingleChar(t *testing.T) {
	s := makeSkill("a", "Desc.", "a")
	result := skill.Validate(s)
	if !result.Valid {
		t.Errorf("single char name should be valid, got errors: %v", result.Errors)
	}
}

func TestValidate_NameWithNumbers(t *testing.T) {
	s := makeSkill("skill-v2", "Desc.", "skill-v2")
	result := skill.Validate(s)
	if !result.Valid {
		t.Errorf("name with numbers should be valid, got errors: %v", result.Errors)
	}
}

func TestValidate_EmptyDescription(t *testing.T) {
	s := makeSkill("test", "", "test")
	result := skill.Validate(s)
	if result.Valid {
		t.Error("expected invalid for empty description")
	}
	assertHasError(t, result, "description", "required")
}

func TestValidate_WhitespaceDescription(t *testing.T) {
	s := makeSkill("test", "   \n\t  ", "test")
	result := skill.Validate(s)
	if result.Valid {
		t.Error("expected invalid for whitespace-only description")
	}
}

func TestValidate_DescriptionTooLong(t *testing.T) {
	longDesc := strings.Repeat("x", 1025)
	s := makeSkill("test", longDesc, "test")
	result := skill.Validate(s)
	if result.Valid {
		t.Error("expected invalid for long description")
	}
	assertHasError(t, result, "description", "1-1024")
}

func TestValidate_NameDirMismatch(t *testing.T) {
	s := makeSkill("skill-a", "Desc.", "skill-b")
	result := skill.Validate(s)
	if result.Valid {
		t.Error("expected invalid for name/dir mismatch")
	}
	assertHasError(t, result, "name", "match directory")
}

func TestValidate_CompatibilityTooLong(t *testing.T) {
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
	result := skill.Validate(s)
	if result.Valid {
		t.Error("expected invalid for long compatibility")
	}
	assertHasError(t, result, "compatibility", "1-500")
}

func TestValidate_ValidCompatibility(t *testing.T) {
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
	result := skill.Validate(s)
	if !result.Valid {
		t.Errorf("valid compatibility should pass, got: %v", result.Errors)
	}
}

// Integration test: load from testdata and validate
func TestValidate_TestdataValidSkill(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "valid-skill")
	s, err := skill.LoadFromPath(dir)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	result := skill.Validate(s)
	if !result.Valid {
		t.Errorf("valid-skill testdata should pass validation, got: %v", result.Errors)
	}
}

func TestValidate_TestdataNoName(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "invalid-skill-no-name")
	s, err := skill.LoadFromPath(dir)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	result := skill.Validate(s)
	if result.Valid {
		t.Error("invalid-skill-no-name should fail")
	}
	assertHasError(t, result, "name", "required")
}

func TestValidate_TestdataNameMismatch(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "invalid-skill-name-mismatch")
	s, err := skill.LoadFromPath(dir)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	result := skill.Validate(s)
	if result.Valid {
		t.Error("invalid-skill-name-mismatch should fail")
	}
	assertHasError(t, result, "name", "match directory")
}

func TestValidate_TestdataEmptyDesc(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "invalid-skill-empty-desc")
	s, err := skill.LoadFromPath(dir)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	result := skill.Validate(s)
	if result.Valid {
		t.Error("empty-desc should fail")
	}
}

func TestValidate_TestdataConsecutiveHyphens(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "invalid-skill-consecutive-hyphens")
	s, err := skill.LoadFromPath(dir)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	result := skill.Validate(s)
	if result.Valid {
		t.Error("consecutive-hyphens should fail")
	}
}

func TestValidate_TestdataStartsHyphen(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "invalid-skill-starts-hyphen")
	s, err := skill.LoadFromPath(dir)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	result := skill.Validate(s)
	if result.Valid {
		t.Error("starts-hyphen should fail")
	}
}

func TestValidate_TestdataUppercase(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "invalid-skill-uppercase")
	s, err := skill.LoadFromPath(dir)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	result := skill.Validate(s)
	if result.Valid {
		t.Error("uppercase should fail")
	}
}

// Multiple errors at once
func TestValidate_MultipleErrors(t *testing.T) {
	s := makeSkill("", "", "some-dir")
	result := skill.Validate(s)
	if result.Valid {
		t.Error("expected invalid")
	}
	if len(result.Errors) < 2 {
		t.Errorf("expected at least 2 errors, got %d", len(result.Errors))
	}
}
