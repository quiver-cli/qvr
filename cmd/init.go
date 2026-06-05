package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/quiver-cli/qvr/internal/canonical"
	"github.com/quiver-cli/qvr/internal/config"
	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/output"
	"github.com/quiver-cli/qvr/internal/registry"
	"github.com/quiver-cli/qvr/internal/skill"
	"github.com/spf13/cobra"
)

var (
	initType       string
	initTarget     string
	initStandalone bool
	initGlobal     bool
)

var initCmd = &cobra.Command{
	Use:   "init <name>",
	Short: "Scaffold a new skill in the project (or as a standalone dir)",
	Long: `Create a new skill conforming to the agentskills.io specification.

<name> must be lowercase alphanumeric and hyphens only, 1-64 chars, and
will become both the directory name and the SKILL.md frontmatter name
(issue #91 — the rule used to be enforced silently).

Default behavior (project-scoped):
  Scaffolds under the canonical agent target dir (alphabetical-first
  installed target, e.g. .claude/skills/<name>/) and seeds an edit-mode
  lock entry. After this, ` + "`qvr publish <name> --tag v0.1.0 --fork <url> --migrate`" + `
  ships the skill to a new git remote.

Pass --standalone to revert to the legacy "create a free-standing dir in cwd"
behavior (useful when you're going to copy the dir into a multi-skill
registry repo before publishing).

Types:
  simple   Just SKILL.md (default)
  medium   SKILL.md + scripts/ + references/ + assets/
  complex  SKILL.md + rules/ + scripts/ + references/ + metadata.json`,
	Args: cobra.ExactArgs(1),
	RunE: runInit,
}

func init() {
	initCmd.Flags().StringVar(&initType, "type", "simple", "skill type (simple|medium|complex)")
	initCmd.Flags().StringVar(&initTarget, "target", "claude", "agent target to scaffold into (project-scoped mode)")
	initCmd.Flags().BoolVar(&initStandalone, "standalone", false, "scaffold a free-standing dir at ./<name> instead of into a project target")
	initCmd.Flags().BoolVar(&initGlobal, "global", false, "(project-scoped mode) seed the lock entry into the user-global lock")
	rootCmd.AddCommand(initCmd)
}

func runInit(cmd *cobra.Command, args []string) error {
	name := args[0]

	if err := validateSkillName(name); err != nil {
		return err
	}

	switch initType {
	case "simple", "medium", "complex":
	default:
		return fmt.Errorf("unknown type %q (use simple, medium, or complex)", initType)
	}

	if initStandalone {
		return runInitStandalone(name)
	}
	_ = cmd
	return runInitProjectScoped(name)
}

// runInitStandalone preserves the pre-v0.7 behavior: create a free-standing
// directory at ./<name>. Useful when the caller intends to copy the scaffolded
// dir into a multi-skill registry repo before publishing.
func runInitStandalone(name string) error {
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
	if err := scaffoldSkillContent(dir, name); err != nil {
		return err
	}
	// Make the scaffolded dir a real git repo so the advertised publish flow
	// (`qvr publish ./<name> --fork <url>`) round-trips without manual git
	// plumbing (issue #150).
	if err := skill.InitRepoWithCommit(dir, fmt.Sprintf("Initialize skill %s", name), "", ""); err != nil {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("init skill repo: %w", err)
	}
	if printer.Format == output.FormatJSON {
		return printer.JSON(map[string]any{
			"name": name,
			"path": dir,
			"type": initType,
			"mode": "standalone",
		})
	}
	printer.Success(fmt.Sprintf("Created %s skill at %s (standalone)", initType, name))
	printer.Info(fmt.Sprintf("  Next: `qvr publish ./%s --registry <name> --tag v0.1.0` (multi-skill registry)", name))
	printer.Info(fmt.Sprintf("        or `qvr publish ./%s --tag v0.1.0 --fork <git-url> --migrate` (single-skill repo)", name))
	return nil
}

// runInitProjectScoped scaffolds into <projectRoot>/<target.LocalDir>/<name>/
// and seeds an edit-mode lock entry so `qvr publish` knows the skill exists
// without a separate `qvr edit`. The freshly-created dir IS the canonical
// edit copy from birth — no shared worktree, no symlink.
func runInitProjectScoped(name string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	t, ok := model.Targets[initTarget]
	if !ok {
		return fmt.Errorf("unknown target %q (try claude|cursor|copilot|codex|windsurf|project)", initTarget)
	}

	canonicalRel := filepath.Join(t.LocalDir, name)
	canonicalAbs := filepath.Join(projectRoot, canonicalRel)
	if _, err := os.Stat(canonicalAbs); err == nil {
		return fmt.Errorf("%s already exists — pick a different name or pass --standalone", canonicalAbs)
	}
	if err := os.MkdirAll(canonicalAbs, 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	if err := scaffoldSkillContent(canonicalAbs, name); err != nil {
		return err
	}
	// Make the edit dir a real git repo from birth — like a worktree-ejected
	// edit dir — so `qvr publish <name> --fork <url>` (the flow this command
	// advertises below) works with no manual git init (issue #150).
	if err := skill.InitRepoWithCommit(canonicalAbs, fmt.Sprintf("Initialize skill %s", name), "", ""); err != nil {
		_ = os.RemoveAll(canonicalAbs)
		return fmt.Errorf("init skill repo: %w", err)
	}

	// Compute subtree hash for the lock entry — gives drift detection a
	// baseline from the moment of scaffold.
	subtreeHash, _ := canonical.HashSubtreeFromDisk(canonicalAbs)

	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), initGlobal)
	lockErr := model.WithLock(config.Dir(), lockPath, func() error {
		lock, lerr := model.ReadLockFile(lockPath)
		if lerr != nil {
			return fmt.Errorf("read lock: %w", lerr)
		}
		if _, gerr := lock.Get(name); gerr == nil {
			return fmt.Errorf("skill %q already in lock — pick a different name or remove the existing entry first", name)
		}
		entry := &model.LockEntry{
			Name:        name,
			Source:      "", // greenfield — no upstream yet
			Ref:         "main",
			Mode:        model.ModeEdit,
			EditPath:    canonicalRel,
			SubtreeHash: subtreeHash,
			Targets:     []string{initTarget},
			InstalledAt: time.Now().UTC(),
		}
		lock.Put(entry)
		if err := lock.Write(); err != nil {
			return fmt.Errorf("write lock: %w", err)
		}
		return nil
	})
	if lockErr != nil {
		// Roll back the dir if we couldn't persist the lock — otherwise
		// the user has a half-init'd skill that subsequent `qvr init` will
		// refuse on the "already exists" check.
		_ = os.RemoveAll(canonicalAbs)
		return lockErr
	}
	registry.TouchProject(lockPath)

	if printer.Format == output.FormatJSON {
		return printer.JSON(map[string]any{
			"name":      name,
			"path":      canonicalAbs,
			"edit_path": canonicalRel,
			"type":      initType,
			"target":    initTarget,
			"mode":      "project",
		})
	}
	printer.Success(fmt.Sprintf("Created %s skill at %s", initType, canonicalRel))
	printer.Info(fmt.Sprintf("  Edit %s/SKILL.md, then:", canonicalRel))
	printer.Info(fmt.Sprintf("    qvr publish %s --tag v0.1.0 --fork git@github.com:you/%s.git --migrate", name, name))
	return nil
}

// scaffoldSkillContent writes SKILL.md and any type-specific extras into dir.
// Shared between standalone and project-scoped modes.
func scaffoldSkillContent(dir, name string) error {
	if err := writeSkillMD(dir, name); err != nil {
		return err
	}
	switch initType {
	case "medium":
		return scaffoldMedium(dir)
	case "complex":
		return scaffoldComplex(dir, name)
	}
	return nil
}

func validateSkillName(name string) error {
	if len(name) == 0 || len(name) > 64 {
		return fmt.Errorf("name must be 1-64 characters")
	}
	for _, c := range name {
		// Reject uppercase explicitly so the user sees the rule rather than a
		// generic "bad character" message (the v0.6.1 error was opaque —
		// issue #91). The agentskills.io spec mandates lowercase.
		if c >= 'A' && c <= 'Z' {
			return fmt.Errorf("name %q has uppercase characters — names must be lowercase (try %q)",
				name, strings.ToLower(name))
		}
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
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
