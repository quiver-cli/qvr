package security

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/pkg/skillspec"
)

// mustWriteFile writes content at dir/rel, creating parent directories
// as needed. Path separators in rel are normalised before joining.
func mustWriteFile(t *testing.T, dir, rel, content string) string {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir parents for %s: %v", full, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
	return full
}

// mustWriteExecutable is mustWriteFile with mode 0o755 so the
// permissions check has something to flag.
func mustWriteExecutable(t *testing.T, dir, rel, content string) string {
	t.Helper()
	p := mustWriteFile(t, dir, rel, content)
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatalf("chmod %s: %v", p, err)
	}
	return p
}

// makeSkill constructs an in-memory *model.Skill for tests that don't
// need to round-trip through disk. The Dir field is filled in but no
// files are materialised.
func makeSkill(name, description, body string, opts ...skillOpt) *model.Skill {
	s := &model.Skill{
		Skill: skillspec.Skill{
			Frontmatter: skillspec.Frontmatter{Name: name, Description: description},
			Body:        body,
		},
		Name: name,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

type skillOpt func(*model.Skill)

func withAllowedTools(s string) skillOpt {
	return func(sk *model.Skill) { sk.Frontmatter.AllowedTools = s }
}
