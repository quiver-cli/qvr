package model

import (
	"github.com/quiver-cli/qvr/pkg/skillspec"
)

// Skill represents a loaded skill with filesystem metadata.
type Skill struct {
	skillspec.Skill
	Dir   string   `json:"dir"`             // Directory path on disk
	Name  string   `json:"name"`            // Directory name (should match frontmatter name)
	Files []string `json:"files,omitempty"` // Relative file paths in the skill directory
}
