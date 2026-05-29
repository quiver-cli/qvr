package skillspec

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	ErrNoFrontmatter    = errors.New("SKILL.md missing frontmatter delimiters (---)")
	ErrInvalidYAML      = errors.New("SKILL.md frontmatter is not valid YAML")
	ErrEmptyContent     = errors.New("SKILL.md is empty")
	ErrMalformedContent = errors.New("SKILL.md has malformed frontmatter")
)

// rawFrontmatter mirrors Frontmatter but decodes `allowed-tools` as a yaml.Node
// so we can accept either the spec's space-delimited string or the array form
// some upstream registries (e.g. claude-plugin marketplaces) ship. The exposed
// Frontmatter type stays a flat string regardless of input shape.
type rawFrontmatter struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license"`
	Compatibility string            `yaml:"compatibility"`
	Metadata      map[string]string `yaml:"metadata"`
	AllowedTools  yaml.Node         `yaml:"allowed-tools"`
	// ForkedFrom records provenance for skills derived from another via
	// `qvr publish --fork`. Format: "<upstream-url>@<commit-sha>". Optional;
	// round-tripped on parse; never validated.
	ForkedFrom string `yaml:"forked-from"`
}

// Parse parses a SKILL.md file content into a Skill struct.
// It splits on "---" delimiters and parses the YAML frontmatter.
func Parse(content string) (*Skill, error) {
	if strings.TrimSpace(content) == "" {
		return nil, ErrEmptyContent
	}

	// Must start with "---"
	trimmed := strings.TrimLeft(content, "\n\r\t ")
	if !strings.HasPrefix(trimmed, "---") {
		return nil, ErrNoFrontmatter
	}

	// Find the closing "---"
	rest := trimmed[3:] // skip opening "---"
	rest = strings.TrimLeft(rest, " \t")
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	} else if len(rest) > 1 && rest[0] == '\r' && rest[1] == '\n' {
		rest = rest[2:]
	}

	closingIdx := strings.Index(rest, "\n---")
	if closingIdx == -1 {
		// Try Windows line endings
		closingIdx = strings.Index(rest, "\r\n---")
		if closingIdx == -1 {
			return nil, ErrMalformedContent
		}
	}

	yamlContent := rest[:closingIdx]
	body := strings.TrimLeft(rest[closingIdx+4:], "-\r\n") // skip "\n---" and any trailing dashes/newlines

	var raw rawFrontmatter
	if err := yaml.Unmarshal([]byte(yamlContent), &raw); err != nil {
		// Skill authors commonly write `description: TL;DR: ...` with an
		// unquoted colon inside the value. YAML 1.2 disallows this, so the
		// strict parser bails. Fall back to a lenient pass that auto-quotes
		// top-level scalar values containing unescaped `: ` runs and try
		// again. Mirrors the agentskills.io spec's intent without requiring
		// every author to know YAML quoting rules.
		fixed := autoQuoteScalarValues(yamlContent)
		if fixed == yamlContent {
			return nil, ErrInvalidYAML
		}
		if err := yaml.Unmarshal([]byte(fixed), &raw); err != nil {
			return nil, ErrInvalidYAML
		}
	}
	allowed, err := flattenAllowedTools(&raw.AllowedTools)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidYAML, err)
	}

	// YAML folded (`>`) and block (`|`) scalars append a trailing newline by
	// default. That leaks into every downstream consumer (table output, info
	// rows, AGENTS.md) as a stray blank line or awkward wrap. Trim here so the
	// model never carries whitespace callers didn't ask for.
	fm := Frontmatter{
		Name:          strings.TrimSpace(raw.Name),
		Description:   strings.TrimSpace(raw.Description),
		License:       strings.TrimSpace(raw.License),
		Compatibility: strings.TrimSpace(raw.Compatibility),
		Metadata:      raw.Metadata,
		AllowedTools:  allowed,
		ForkedFrom:    strings.TrimSpace(raw.ForkedFrom),
	}

	return &Skill{
		Frontmatter: fm,
		Body:        strings.TrimSpace(body),
		Raw:         content,
	}, nil
}

// scalarFieldLineRe matches a top-level YAML scalar assignment like
// `description: foo` or `name: bar`. Captures the key, the leading
// whitespace before the value, and the value itself. Anchored to the line
// start (no indentation) because we only auto-quote top-level fields —
// nested mappings are left to the user.
var scalarFieldLineRe = regexp.MustCompile(`^([a-zA-Z_][a-zA-Z0-9_-]*):([ \t]+)(\S.*?)\s*$`)

// autoQuoteScalarValues rewrites lines like `description: TL;DR: foo` into
// `description: 'TL;DR: foo'` so the strict YAML parser accepts them.
// Untouched: lines whose value already starts with a quote, a flow opener
// (`{`, `[`), a block scalar indicator (`|`, `>`), an anchor/alias (`&`, `*`),
// or a tag (`!`); empty values; and indented (non-top-level) lines. Returns
// the original string unchanged when no candidates are found, so callers can
// detect "no rewrite happened" cheaply.
func autoQuoteScalarValues(yamlContent string) string {
	lines := strings.Split(yamlContent, "\n")
	changed := false
	for i, line := range lines {
		m := scalarFieldLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key, gap, value := m[1], m[2], m[3]
		// Skip values that are already quoted, special, or flow-style.
		if value == "" {
			continue
		}
		switch value[0] {
		case '"', '\'', '|', '>', '{', '[', '&', '*', '!', '#':
			continue
		}
		// Only quote when there's a `: ` later in the value — that's the
		// substring that breaks the strict parser. Plain values without
		// embedded colons parse fine and quoting them is unnecessary noise.
		if !strings.Contains(value, ": ") {
			continue
		}
		// Single-quote escape: `'` → `''`. We pick single quotes so users
		// don't trip on backslash escapes inside the value.
		quoted := "'" + strings.ReplaceAll(value, "'", "''") + "'"
		lines[i] = key + ":" + gap + quoted
		changed = true
	}
	if !changed {
		return yamlContent
	}
	return strings.Join(lines, "\n")
}

// flattenAllowedTools normalizes `allowed-tools` to the spec's space-delimited
// string form, accepting both the canonical scalar and the array form. Empty
// or missing nodes return "" without error.
func flattenAllowedTools(node *yaml.Node) (string, error) {
	if node == nil || node.Kind == 0 {
		return "", nil
	}
	switch node.Kind {
	case yaml.ScalarNode:
		return strings.TrimSpace(node.Value), nil
	case yaml.SequenceNode:
		items := make([]string, 0, len(node.Content))
		for _, child := range node.Content {
			if child.Kind != yaml.ScalarNode {
				return "", fmt.Errorf("allowed-tools list entries must be strings (got kind %d)", child.Kind)
			}
			v := strings.TrimSpace(child.Value)
			if v != "" {
				items = append(items, v)
			}
		}
		return strings.Join(items, " "), nil
	default:
		return "", fmt.Errorf("allowed-tools must be a string or list of strings")
	}
}
