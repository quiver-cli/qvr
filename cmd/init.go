package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/raks097/quiver/internal/output"
	"github.com/spf13/cobra"
)

var initType string

var initCmd = &cobra.Command{
	Use:   "init <name>",
	Short: "Scaffold a new skill project",
	Long: `Create a new skill directory conforming to the agentskills.io specification.

Types:
  simple   Just SKILL.md (default)
  medium   SKILL.md + scripts/ + references/ + assets/
  complex  SKILL.md + rules/ + scripts/ + references/ + metadata.json`,
	Args: cobra.ExactArgs(1),
	RunE: runInit,
}

func init() {
	initCmd.Flags().StringVar(&initType, "type", "simple", "skill type (simple|medium|complex)")
	rootCmd.AddCommand(initCmd)
}

func runInit(cmd *cobra.Command, args []string) error {
	name := args[0]

	if err := validateSkillName(name); err != nil {
		return err
	}

	dir, err := filepath.Abs(name)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("directory %s already exists", name)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	if err := writeSkillMD(dir, name); err != nil {
		return err
	}

	switch initType {
	case "medium":
		if err := scaffoldMedium(dir); err != nil {
			return err
		}
	case "complex":
		if err := scaffoldComplex(dir, name); err != nil {
			return err
		}
	case "simple":
		// Nothing extra
	default:
		return fmt.Errorf("unknown type %q (use simple, medium, or complex)", initType)
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(map[string]any{
			"name": name,
			"path": dir,
			"type": initType,
		})
	}

	printer.Success(fmt.Sprintf("Created %s skill at %s", initType, name))
	return nil
}

func validateSkillName(name string) error {
	if len(name) == 0 || len(name) > 64 {
		return fmt.Errorf("name must be 1-64 characters")
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return fmt.Errorf("name must contain only lowercase alphanumeric characters and hyphens")
		}
	}
	if name[0] == '-' || name[len(name)-1] == '-' {
		return fmt.Errorf("name must not start or end with a hyphen")
	}
	if strings.Contains(name, "--") {
		return fmt.Errorf("name must not contain consecutive hyphens")
	}
	return nil
}

func writeSkillMD(dir, name string) error {
	content := fmt.Sprintf(`---
name: %s
description: >
  Describe what this skill does and when to use it.
  Include specific keywords that help agents identify relevant tasks.
metadata:
  author: ""
  version: "0.1.0"
---

# %s

## Instructions

1. Step one...
2. Step two...

## Examples

Describe example inputs and expected behavior.

## Edge Cases

Document common edge cases to handle.
`, name, strings.ReplaceAll(name, "-", " "))

	return os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644)
}

func scaffoldMedium(dir string) error {
	dirs := []string{"scripts", "references", "assets"}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}

	gitkeeps := []string{"scripts/.gitkeep", "references/.gitkeep", "assets/.gitkeep"}
	for _, f := range gitkeeps {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(""), 0o644); err != nil {
			return fmt.Errorf("create %s: %w", f, err)
		}
	}
	return nil
}

func scaffoldComplex(dir, name string) error {
	if err := scaffoldMedium(dir); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Join(dir, "rules"), 0o755); err != nil {
		return fmt.Errorf("create rules: %w", err)
	}

	sections := `---
title: Sections
---

## Section 1: Getting Started
Description of the first section.
`
	if err := os.WriteFile(filepath.Join(dir, "rules", "_sections.md"), []byte(sections), 0o644); err != nil {
		return err
	}

	template := `---
title: Rule Title
impact: MEDIUM
tags: example
---

## Rule Title

Brief explanation.

**Incorrect:**
` + "```" + `
// Bad example
` + "```" + `

**Correct:**
` + "```" + `
// Good example
` + "```" + `
`
	if err := os.WriteFile(filepath.Join(dir, "rules", "_template.md"), []byte(template), 0o644); err != nil {
		return err
	}

	metadata := fmt.Sprintf(`{
  "version": "0.1.0",
  "organization": "",
  "date": "",
  "abstract": "Description of %s",
  "references": []
}
`, name)
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(metadata), 0o644); err != nil {
		return err
	}

	buildScript := `#!/bin/bash
set -e
echo "Compiling rules into AGENTS.md..." >&2
cat rules/*.md > AGENTS.md 2>/dev/null || echo "No rules found" >&2
echo "Done." >&2
`
	return os.WriteFile(filepath.Join(dir, "scripts", "build.sh"), []byte(buildScript), 0o755)
}
