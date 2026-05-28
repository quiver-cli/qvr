# Getting Started

> **Status: v0.5.0.** Project-local lockfile model: `qvr init`, `validate`,
> `config`, `registry add/list/update/remove`, `search`, `version`, `add`,
> `remove`, `sync` (strict reconciler), `docs` (AGENTS.md), `pull`, `push`,
> `publish`, `edit`, `upgrade`, `switch`, `link`, `read`, `list`, `ls`,
> `info`, `diff`, `status`, `outdated`, `disable`, `enable`, `doctor`
> (with `--global` and `--all` across inspections). The local React
> dashboard (`qvr ui`) is in flight.

## Install

Prebuilt binaries (Homebrew, curl installer) are still in flight. For
now, build from source or install via `go install`:

```bash
# from source
git clone https://github.com/raks097/quiver.git
cd quiver
make install            # -> /usr/local/bin/qvr

# or directly
go install github.com/raks097/quiver@latest
```

## Quick Start

### 1. Add a registry

```bash
# Add your team's skills repo
qvr registry add team git@github.com:your-org/skills.git

# Or add the community registry
qvr registry add community https://github.com/quiver-sh/community-skills.git
```

### 2. Search for skills

```bash
qvr search deploy
qvr search code-review --output json
```

### 3. Install a skill

```bash
# Install to Claude Code
qvr add code-review --target claude

# Install to multiple agents
qvr add code-review --target claude --target cursor

# Install a specific version
qvr add code-review@v2 --target claude
```

### 4. Use the skill

The skill is now symlinked into your agent's skills directory. The agent will automatically discover and use it.

```bash
# Check what's installed
qvr list

# Read skill content (what the agent sees)
qvr read code-review
```

### 5. Keep skills updated

```bash
# Check for updates
qvr status

# Pull latest changes
qvr pull

# Pull a specific skill
qvr pull code-review
```

### 6. Push changes back

If you (or your agent) modify a skill, push changes upstream:

```bash
qvr status              # See what changed
qvr push code-review -m "improved review patterns"
```

## Create Your First Skill

```bash
# Scaffold a new skill
qvr init my-first-skill

# Edit SKILL.md with your instructions
# ...

# Validate it
qvr validate my-first-skill

# Publish to your registry
cd my-first-skill
qvr publish --registry team
```

## Set Your Defaults

```bash
qvr config set default_target claude
qvr config set default_registry team
```

## Launch the Dashboard

```bash
qvr ui
# → Opens localhost:3000
```

## What's Next

- [Creating a Skill](creating-a-skill.md) — Detailed skill authoring guide
- [Creating a Registry](creating-a-registry.md) — Set up a team registry
- [Team Workflows](team-workflows.md) — Multi-team collaboration
- [Agent Integration](agent-integration.md) — Per-agent setup guide
