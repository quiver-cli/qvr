# Agent Integration Guide

> **Status: active development.** Target paths, `--target <agent>`, `--global`,
> `AGENTS.md` generation, and audit hook installation are available today.

qvr works with any AI coding agent that reads skills from a directory. Run
`qvr target list` for the full set (~60 agents) with their exact local/global
dirs and aliases; the table below covers the common core. Per-skill symlinks
point into the SHA-keyed worktree store at
`~/.quiver/worktrees/<org>/<repo>/<skill>/<sha7>/`.

| Agent | `--target` | Local dir | Global dir |
| ----- | ---------- | --------- | ---------- |
| Claude Code | `claude` | `.claude/skills` | `~/.claude/skills` |
| Cursor | `cursor` | `.agents/skills` | `~/.cursor/skills` |
| GitHub Copilot | `copilot` | `.github/skills` | `~/.copilot/skills` |
| OpenAI Codex CLI | `codex` | `.agents/skills` | `~/.agents/skills` |
| Gemini CLI / Antigravity | `gemini` | `.agents/skills` | `~/.gemini/skills` |
| Hermes | `hermes` | `.hermes/skills` | `~/.hermes/skills` |
| OpenClaw | `openclaw` | `.agents/skills` | `~/.openclaw/skills` |
| Generic AGENTS.md | `project` | `.agents/skills` | `~/.agents/skills` |

> Many newer CLIs share the AGENTS.md `.agents/skills` project location, so one
> install can serve several agents at once.

## Claude Code

```bash
# Install a skill for Claude Code
qvr add code-review --target claude

# Global install (available in all projects)
qvr add code-review --target claude --global

# Verify the symlink
ls -la .claude/skills/
# code-review -> ~/.quiver/worktrees/acme-labs/agent-skills/code-review/abc1234
```

Claude Code automatically discovers skills at startup (loads name + description) and activates them on demand (loads full SKILL.md).

## Cursor / Codex / Gemini (shared `.agents/skills`)

```bash
qvr add code-review --target cursor
qvr add code-review --target codex
qvr add code-review --target gemini
```

These write into the project's `.agents/skills/` directory (their globals differ —
see the table). Installing for several at once is a single symlink set.

## GitHub Copilot

```bash
qvr add code-review --target copilot     # -> .github/skills/code-review
```

## OpenClaw

```bash
qvr add code-review --target openclaw            # -> .agents/skills/code-review
qvr add code-review --target openclaw --global   # -> ~/.openclaw/skills/code-review
```

OpenClaw loads `<workspace>/.agents/skills` as project agent skills; the global
lane targets `~/.openclaw/skills`, its shared managed-skills directory
([docs](https://docs.openclaw.ai/tools/skills)).

## Hermes

```bash
qvr add code-review --target hermes --global     # -> ~/.hermes/skills/code-review
```

`~/.hermes/skills` is Hermes' single source of truth, so global installs work
out of the box. For project-local installs (`.hermes/skills`), add that
directory to Hermes' `skills.external_dirs` config so it gets scanned
([docs](https://hermes-agent.nousresearch.com/docs/user-guide/features/skills)).

## Generic / Other Agents

For agents not called out above, use `project` (the AGENTS.md convention):

```bash
qvr add code-review --target project
# → .agents/skills/code-review
```

To set a project's default agents once (recorded in `qvr.toml`), use
`qvr target add` instead of passing `--target` every time:

```bash
qvr target add claude cursor
```

## Multi-Agent Install

Install the same skill for multiple agents at once:

```bash
qvr add code-review --target claude --target cursor --target copilot
```

All three agents now share the same skill (same source, via symlinks). Changes to the skill are visible to all agents simultaneously.

## Inspecting what an agent sees

There's no read-through command — the agent reads the symlinked `SKILL.md`
directly. To inspect an install from the CLI:

```bash
qvr info code-review        # frontmatter, refs, targets, provenance (local-only, fast)
qvr ls code-review          # files bundled with the skill (-r to recurse)
```

## AGENTS.md Generation

Generate an AGENTS.md file that lists all installed skills for agent discovery:

```bash
qvr docs                    # writes ./AGENTS.md from the project lock
qvr docs -o docs/AGENTS.md  # custom output path
qvr docs --global           # generate from the user-global lock instead
```

The generated AGENTS.md includes:
- Available skills with names and descriptions
- Invocation instructions
- Base directory hints for resolving references

## Recommended Workflow

1. **Pick your agents**: `qvr target add claude cursor` (recorded in `qvr.toml`)
2. **Install team skills**: `qvr add code-review deploy-helper test-runner`
3. **Generate AGENTS.md**: `qvr docs` (optional, depends on agent)
4. **Reconcile against the lock**: `qvr sync` (removes orphan symlinks, rebuilds missing worktrees from the lock + bare clone)
5. **Keep updated**: `qvr outdated`, then `qvr switch <skill> --tip` (or `qvr pull`)
6. **Push improvements**: if the agent modifies a skill, `qvr edit <skill>` → `qvr publish <skill>`
