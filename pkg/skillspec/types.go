// Package skillspec provides a public SKILL.md parser conforming to the agentskills.io specification.
// External tools (IDE plugins, CI actions) can import this package independently.
package skillspec

// Frontmatter represents the YAML frontmatter of a SKILL.md file.
// See https://agentskills.io/specification for the full spec.
type Frontmatter struct {
	Name          string            `yaml:"name" json:"name"`
	Description   string            `yaml:"description" json:"description"`
	License       string            `yaml:"license,omitempty" json:"license,omitempty"`
	Compatibility string            `yaml:"compatibility,omitempty" json:"compatibility,omitempty"`
	Metadata      map[string]string `yaml:"metadata,omitempty" json:"metadata,omitempty"`
	AllowedTools  string            `yaml:"allowed-tools,omitempty" json:"allowed_tools,omitempty"`
}

// Skill represents a fully parsed SKILL.md file.
type Skill struct {
	Frontmatter Frontmatter `json:"frontmatter"`
	Body        string      `json:"body"`
	Raw         string      `json:"-"` // Unparsed SKILL.md content for hashing/display
}
