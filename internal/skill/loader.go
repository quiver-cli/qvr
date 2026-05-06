package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/pkg/skillspec"
)

// LoadFromPath loads a skill from a directory path.
func LoadFromPath(dir string) (*model.Skill, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}

	info, err := os.Stat(absDir)
	if err != nil {
		return nil, fmt.Errorf("stat skill dir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", absDir)
	}

	skillMDPath := filepath.Join(absDir, "SKILL.md")
	content, err := os.ReadFile(skillMDPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("SKILL.md not found in %s", absDir)
		}
		return nil, fmt.Errorf("read SKILL.md: %w", err)
	}

	parsed, err := skillspec.Parse(string(content))
	if err != nil {
		return nil, fmt.Errorf("parse SKILL.md: %w", err)
	}

	files, err := listFiles(absDir)
	if err != nil {
		return nil, fmt.Errorf("list files: %w", err)
	}

	return &model.Skill{
		Skill: *parsed,
		Dir:   absDir,
		Name:  filepath.Base(absDir),
		Files: files,
	}, nil
}

// listFiles returns relative paths of all files in the skill directory.
func listFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		// Skip OS metadata files
		base := filepath.Base(rel)
		if base == ".DS_Store" || base == "Thumbs.db" {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// HasFile checks if a skill contains a file at the given relative path.
func HasFile(s *model.Skill, relPath string) bool {
	for _, f := range s.Files {
		if strings.EqualFold(f, relPath) {
			return true
		}
	}
	return false
}
