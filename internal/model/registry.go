package model

import "time"

// Registry represents a fully resolved registry with runtime state.
type Registry struct {
	Name          string    `json:"name"`
	URL           string    `json:"url"`
	Path          string    `json:"path"`
	SkillCount    int       `json:"skill_count"`
	SkippedCount  int       `json:"skipped_count,omitempty"`
	LastFetched   time.Time `json:"last_fetched"`
	DefaultBranch string    `json:"default_branch"`

	// CredentialsStripped is true when the user supplied a URL with
	// embedded credentials and we persisted the sanitised form. Callers
	// can surface a warning so the user knows their token isn't being
	// stored — and should live in a credential helper instead.
	CredentialsStripped bool `json:"credentials_stripped,omitempty"`
}

// SkippedSkill records a candidate skill directory that the indexer could not
// register (no SKILL.md, malformed frontmatter, etc). Surfaced via
// RegistryStatus so the CLI can distinguish empty registries from broken ones.
type SkippedSkill struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// RegistryStatus represents sync state for display. SkippedCount lives on the
// embedded Registry; Skipped carries per-skill detail when the caller needs to
// render reasons (verbose update output, JSON consumers).
type RegistryStatus struct {
	Registry
	HasUpstreamChanges bool           `json:"has_upstream_changes"`
	Skipped            []SkippedSkill `json:"skipped,omitempty"`
	Error              string         `json:"error,omitempty"`
}

// StandaloneRepo represents a directly cloned single-skill repo.
type StandaloneRepo struct {
	URL  string `json:"url"`
	Path string `json:"path"`
	Slug string `json:"slug"`
}
