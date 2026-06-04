# Agent Integration Guide

> **Status: active development.** Target paths, `--target <agent>`, `--global`,
> `AGENTS.md` generation, and audit hook installation are available today.

qvr works with any AI coding agent that reads skills from a directory. Here's how to set up each agent.

## Claude Code

Claude Code reads skills from `.claude/skills/` (local) or `~/.claude/skills/` (global).

```bash
# Install a skill for Claude Code
qvr add code-review --target claude

# Global install (available in all projects)
qvr add code-review --target claude --global

# Verify
ls -la .claude/skills/
# code-review -> ~/.quiver/worktrees/acme--code-review--main/skills/code-review
```

Claude Code automatically discovers skills at startup (loads name + description) and activates them on demand (loads full SKILL.md).

## Cursor

Cursor reads rules from `.cursor/rules/` (local) or `~/.cursor/rules/` (global).

```bash
# Install for Cursor
qvr add code-review --target cursor

# Global
qvr add code-review --target cursor --global

# Verify
ls -la .cursor/rules/
# code-review -> ~/.quiver/worktrees/acme--code-review--main/skills/code-review
```

## GitHub Copilot

Copilot reads from `.github/copilot/skills/` (local) or `~/.github/copilot/skills/` (global).

```bash
qvr add code-review --target copilot
```

## OpenAI Codex CLI

Codex reads from `.codex/skills/` (local) or `~/.codex/skills/` (global).

```bash
qvr add code-review --target codex
```

## Windsurf

Windsurf reads from `.windsurf/skills/` (local) or `~/.windsurf/skills/` (global).

```bash
qvr add code-review --target windsurf
```

## Generic / Other Agents

For agents not in the built-in target list, use `project`:

```bash
qvr add code-review --target project
# → .agent/skills/code-review
```

Or use `link` to symlink to any custom path:

```bash
qvr link ~/.quiver/worktrees/acme--code-review--main/skills/code-review /path/to/agent/skills/code-review
```

## Multi-Agent Install

Install the same skill for multiple agents at once:

```bash
qvr add code-review --target claude --target cursor --target copilot
```

All three agents now share the same skill (same source, via symlinks). Changes to the skill are visible to all agents simultaneously.

## Agent Output Format

When agents invoke `qvr read`:

```bash
qvr read code-review
# Outputs:
# SKILL: code-review
# BASE_DIR: /Users/you/.quiver/worktrees/acme--code-review--main/skills/code-review
# ---
# (full SKILL.md content)

qvr read code-review --output json
# {
#   "name": "code-review",
#   "baseDir": "/Users/you/.quiver/worktrees/...",
#   "content": "...",
#   "metadata": { "author": "acme" }
# }
```

## AGENTS.md Generation

Generate an AGENTS.md file that lists all installed skills for agent discovery:

```bash
qvr docs
# Creates AGENTS.md in current directory

qvr docs --output ./AGENTS.md --target claude
```

The generated AGENTS.md includes:
- Available skills with names and descriptions
- Invocation instructions
- Base directory hints for resolving references

## Recommended Workflow

1. **Set your default target**: `qvr config set default_target claude`
2. **Install team skills**: `qvr add code-review deploy-helper test-runner`
3. **Generate AGENTS.md**: `qvr docs` (optional, depends on agent)
3a. **Reconcile project against the lock**: `qvr sync` (removes orphan symlinks, rebuilds missing worktrees from the lock + bare clone)
4. **Keep updated**: `qvr pull` periodically
5. **Push improvements**: If agent modifies a skill, `qvr push <skill>`
