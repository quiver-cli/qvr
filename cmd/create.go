package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/astra-sh/qvr/internal/canonical"
	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/git"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/astra-sh/qvr/internal/registry"
	"github.com/astra-sh/qvr/internal/skill"
	"github.com/spf13/cobra"
)

var (
	createType       string
	createTarget     string
	createStandalone bool
	createGlobal     bool
)

var createCmd = &cobra.Command{
	Use:   "create <name>",
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

To initialize a project without creating a skill, use ` + "`qvr init`" + ` instead.

Types:
  simple   Just SKILL.md (default)
  medium   SKILL.md + scripts/ + references/ + assets/
  complex  SKILL.md + rules/ + scripts/ + references/ + metadata.json`,
	Args: cobra.ExactArgs(1),
	RunE: runCreate,
}

func init() {
	createCmd.Flags().StringVar(&createType, "type", "simple", "skill type (simple|medium|complex)")
	createCmd.Flags().StringVar(&createTarget, "target", "claude", "agent target to scaffold into (project-scoped mode)")
	createCmd.Flags().BoolVar(&createStandalone, "standalone", false, "scaffold a free-standing dir at ./<name> instead of into a project target")
	createCmd.Flags().BoolVar(&createGlobal, "global", false, "(project-scoped mode) seed the lock entry into the user-global lock")
	rootCmd.AddCommand(createCmd)
}

func runCreate(cmd *cobra.Command, args []string) error {
	name := args[0]

	if err := validateSkillName(name); err != nil {
		return err
	}

	switch createType {
	case "simple", "medium", "complex":
	default:
		return fmt.Errorf("unknown type %q (use simple, medium, or complex)", createType)
	}

	if createStandalone {
		return runCreateStandalone(name)
	}
	_ = cmd
	return runCreateProjectScoped(name)
}

// runCreateStandalone preserves the pre-v0.7 behavior: create a free-standing
// directory at ./<name>. Useful when the caller intends to copy the scaffolded
// dir into a multi-skill registry repo before publishing.
func runCreateStandalone(name string) error {
	dir, err := filepath.Abs(name)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	// Refuse the gitlink trap (#241): a standalone scaffold git-inits its own
	// repo, and inside an existing work tree `git add` records that nested
	// repo as a gitlink (an empty pointer commit) — the skill's files never
	// reach the outer repo's history, and a registry built from it indexes
	// nothing.
	if root, inRepo := git.EnclosingWorkTree(filepath.Dir(dir)); inRepo {
		return fmt.Errorf("refusing to create a standalone skill inside the git work tree at %s: the scaffold initializes its own nested repo, which `git add` records as a gitlink (an empty pointer commit) instead of the skill's files. Drop --standalone to scaffold into the project, or run this outside the repository", root)
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
			"type": createType,
			"mode": "standalone",
		})
	}
	printer.Success(fmt.Sprintf("Created %s skill at %s (standalone)", createType, name))
	printer.Hint(fmt.Sprintf("publish with `qvr publish ./%s --registry <name> --tag v0.1.0` (multi-skill registry)", name))
	printer.Hint(fmt.Sprintf("or `qvr publish ./%s --tag v0.1.0 --fork <git-url> --migrate` (single-skill repo)", name))
	return nil
}

// runCreateProjectScoped scaffolds into <projectRoot>/<target.LocalDir>/<name>/
// and seeds an edit-mode lock entry so `qvr publish` knows the skill exists
// without a separate `qvr edit`. The freshly-created dir IS the canonical
// edit copy from birth — no shared worktree, no symlink.
func runCreateProjectScoped(name string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	t, ok := model.LookupTarget(createTarget)
	if !ok {
		return fmt.Errorf("unknown target %q (run `qvr target list` to see all supported agents)", createTarget)
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

	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), createGlobal)
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
			Targets:     []string{createTarget},
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
		// the user has a half-created skill that subsequent `qvr create` will
		// refuse on the "already exists" check.
		_ = os.RemoveAll(canonicalAbs)
		return lockErr
	}
	registry.TouchProject(lockPath)

	// Scaffold qvr.toml (the declarative front door) so the project starts with
	// a committable config carrying its default agent target. Skipped for the
	// global lane (no project file) and never clobbers an existing file.
	if !createGlobal {
		if perr := scaffoldProjectFile(projectRoot); perr != nil {
			printer.Warning(fmt.Sprintf("created the skill but failed to scaffold qvr.toml (%v)", perr))
		}
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(map[string]any{
			"name":      name,
			"path":      canonicalAbs,
			"edit_path": canonicalRel,
			"type":      createType,
			"target":    createTarget,
			"mode":      "project",
		})
	}
	printer.Success(fmt.Sprintf("Created %s skill at %s", createType, canonicalRel))
	printer.Hint(fmt.Sprintf("edit %s/SKILL.md, then run `qvr publish %s --tag v0.1.0 --fork git@github.com:you/%s.git --migrate`", canonicalRel, name, name))
	return nil
}

// scaffoldProjectFile writes a starter qvr.toml at the project root if one does
// not already exist (the side effect of `qvr create`). It seeds [project] with
// the directory name, a 0.1.0 version, and the create target as the default.
// Existing files are never clobbered — adoption beyond first scaffold flows
// through `qvr add` / `qvr target`. The richer, target-inferring path lives in
// `qvr init` (cmd/init.go); both share writeScaffoldedProjectFile.
func scaffoldProjectFile(projectRoot string) error {
	projPath := model.DefaultProjectPath(projectRoot)
	if _, err := os.Stat(projPath); err == nil {
		return nil // already exists — leave it alone
	} else if !os.IsNotExist(err) {
		return err
	}
	targets := []string{createTarget}
	if canonicalName, ok := model.CanonicalTarget(createTarget); ok {
		targets = []string{canonicalName}
	}
	return writeScaffoldedProjectFile(projPath, filepath.Base(projectRoot), "0.1.0", targets)
}

// writeScaffoldedProjectFile writes a starter qvr.toml at projPath with the
// given name, version, and default targets, prefixed by the reserved-section
// comment banner. The caller owns the "already exists" guard. An empty
// defaultTargets slice renders no default-targets line (omitempty), leaving a
// minimal-but-well-formed file. Shared by `qvr create` and `qvr init`.
func writeScaffoldedProjectFile(projPath, name, version string, defaultTargets []string) error {
	proj := model.NewProjectFile(projPath)
	proj.Project.Name = name
	proj.Project.Version = version
	proj.Project.DefaultTargets = defaultTargets
	body, err := model.MarshalProjectFile(proj)
	if err != nil {
		return err
	}
	const banner = "# qvr.toml — declarative project config (the front door).\n" +
		"# qvr.lock remains the resolved, self-sufficient lockfile.\n" +
		"# Skills you add with `qvr add` are recorded under [skills].\n" +
		"# Reserved for future milestones (documented but inert):\n" +
		"#   [plugins]   # plugins -> skills expansion\n" +
		"#   [hooks]     # lifecycle hooks\n" +
		"#   [mcp]       # MCP servers\n\n"
	return os.WriteFile(projPath, append([]byte(banner), body...), 0o644)
}

// scaffoldSkillContent writes SKILL.md and any type-specific extras into dir.
// Shared between standalone and project-scoped modes.
func scaffoldSkillContent(dir, name string) error {
	if err := writeSkillMD(dir, name); err != nil {
		return err
	}
	switch createType {
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
