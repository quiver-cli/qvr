# Getting Started

> **Status: active development.** Project-local locks, registry management,
> install/sync/edit/publish, security scan gates, inspections, audit capture,
> and the local dashboard (`qvr ui`) are available today.

## Install

The recommended path is the prebuilt binary — a single self-contained file with
the dashboard baked in:

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/astra-sh/qvr/main/install.sh | sh

# Windows (PowerShell)
irm https://raw.githubusercontent.com/astra-sh/qvr/main/install.ps1 | iex
```

Or build from source (requires Go 1.25+ and Node 20+):

```bash
git clone https://github.com/astra-sh/qvr.git
cd qvr
make build              # builds the React UI, then embeds it into the binary
make install            # -> /usr/local/bin/qvr
```

> Plain `go install` is **not** supported — it can't run the npm build, so it
> ships without the dashboard. Use the installer or `make build`.

## Quick Start

### 1. Add a registry

Any git clone URL works; the name is inferred as `<org>/<repo>`.

```bash
# Add your team's skills repo  (-> your-org/skills)
qvr registry add git@github.com:your-org/skills.git

# Or add the community registry  (-> astra-sh/community-skills)
qvr registry add https://github.com/astra-sh/community-skills.git
```

### 2. Search for skills

```bash
qvr search deploy
qvr search code-review --output json
```

### 3. Pick your agents (once per project)

`qvr target add` records the agents this project installs into, written to
`qvr.toml`'s `[project].default-targets` so the choice travels with the repo:

```bash
qvr target add claude cursor     # bare `qvr add` now routes to both
qvr target list                  # all supported agents + this project's defaults
```

### 4. Install a skill

Installing writes both files — `qvr.toml` (your declared intent) and `qvr.lock`
(the resolved, scanned proof) — and symlinks the skill into your target agent dirs:

```bash
qvr add code-review              # uses the project's default-targets
qvr add code-review@v2           # pin a tag, branch, or SHA
qvr add code-review --target cursor   # override targets for this one install
```

### 5. Use the skill

The skill is now symlinked into each target agent's skills directory; the agent
discovers it automatically (name + description at startup, full `SKILL.md` on
activation).

```bash
qvr list                 # what's installed (from the lock)
qvr info code-review     # frontmatter, refs, targets, provenance
qvr tree                 # grouped by registry / target
```

### 6. Keep skills updated

```bash
qvr outdated                       # installs with newer upstream commits
qvr switch code-review --tip       # fast-forward the current ref to upstream tip
qvr switch code-review --latest    # jump to the newest semver tag
qvr pull                           # fast-forward every skill (alias of switch --tip)
```

### 7. Push changes back

If you (or your agent) modify a skill, eject it and publish — `qvr publish`
re-runs the lint + scan gate and never touches the remote until it passes:

```bash
qvr edit code-review                       # symlink -> real, editable dir
qvr diff code-review                       # review local changes
qvr publish code-review -m "improved review patterns"
```

## Create Your First Skill

```bash
# (Optional) bootstrap the project front door — writes qvr.toml, infers targets
qvr init

# Scaffold a new skill — written into your default target dir,
# e.g. .claude/skills/my-first-skill
qvr create my-first-skill

# Edit .claude/skills/my-first-skill/SKILL.md with your instructions
# ...

# Lint it
qvr lint my-first-skill

# Publish to your registry (the full <org>/<repo> name)
qvr publish my-first-skill --registry your-org/skills
```

To give the skill its own single-skill repo instead, publish with
`qvr publish my-first-skill --fork <git-url> --migrate --tag v0.1.0`.

## Set Your Defaults

Per-project agent routing lives in `qvr.toml` (`qvr target add`, above). For
machine-local fallbacks, use `qvr config`:

```bash
qvr config set default_target claude       # fallback target when a project has none
qvr config set default_registry your-org/skills
```

## Reproduce & gate in CI

`qvr.lock` is committed and self-sufficient — a fresh clone replays it to the
byte-identical, already-vetted set:

```bash
qvr sync                 # reconcile the working tree against the lock
qvr sync --locked        # CI: restore from the lock, fail if it would change
qvr lock verify --strict # CI: fail if anything on disk drifts from the lock
```

## Launch the Dashboard

```bash
qvr ui
# → serves http://127.0.0.1:7878 (embedded in the binary)
```

## What's Next

- [Creating a Skill](creating-a-skill.md) — Detailed skill authoring guide
- [Creating a Registry](creating-a-registry.md) — Set up a team registry
- [Team Workflows](team-workflows.md) — Multi-team collaboration
- [Agent Integration](agent-integration.md) — Per-agent setup guide
