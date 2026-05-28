# Registry Format

A registry is a Git repository that contains skills. qvr clones registries as bare repos and uses worktrees for installed skills.

## Repository Structure

```
acme-skills/
├── registry.yaml                    # Registry metadata (required)
├── skills/                          # Skills directory
│   ├── code-review/
│   │   └── SKILL.md
│   ├── deploy-helper/
│   │   ├── SKILL.md
│   │   ├── scripts/deploy.sh
│   │   └── references/GUIDE.md
│   └── test-runner/
│       └── SKILL.md
├── TEAMS.yaml                       # Optional: team definitions
└── README.md                        # Optional: human-readable docs
```

## registry.yaml

Required metadata file at the repository root:

```yaml
name: acme-skills
description: ACME Corp's curated agent skills
maintainers:
  - name: Platform Team
    email: platform@acme.com
  - name: Alice Smith
    github: alicesmith
settings:
  require-scan: true                 # Require scan before publish
  default-branch: main               # Default branch for skill resolution
```

### Fields

| Field | Required | Description |
|-------|----------|-------------|
| `name` | Yes | Registry identifier |
| `description` | Yes | Human-readable description |
| `maintainers` | No | List of maintainers with name and contact |
| `settings.require-scan` | No | Whether skills must pass scan before publish (default: false) |
| `settings.default-branch` | No | Default branch for version resolution (default: main) |

## Versioning Model

Skills are versioned via **Git branches and tags**:

- **Branches** = development versions (`main`, `v2`, `experimental`)
- **Tags** = release versions (`v1.0.0`, `v1.1.0`, `v2.0.0`)
- **`main`** (or default branch) = latest stable

```bash
# Install latest (default branch)
qvr add code-review

# Install specific branch
qvr add code-review@v2

# Install specific tag
qvr add code-review@v1.0.0
```

**Resolution order**: exact tag → exact branch → error.

## TEAMS.yaml (Optional)

Defines team structure for collaboration. See [team-workflows.md](guides/team-workflows.md).

```yaml
teams:
  platform:
    description: Platform engineering team
    members:
      - github: alice
        role: maintainer
      - github: bob
        role: contributor
    skills:
      - deploy-helper
      - infra-scanner
```

## Standalone Skill Repos

A skill can also live in its own Git repository (not a registry):

```
my-skill/
├── SKILL.md
├── scripts/
└── references/
```

Added via `qvr add <repo-url>` instead of `qvr registry add`.

## How qvr Uses Registries

1. **Add**: `qvr registry add acme git@github.com:acme/skills.git`
   - Bare clone to `~/.quiver/registries/acme.git/`

2. **Index**: qvr reads git tree objects from the bare repo to discover skills
   - No checkout needed — reads blob objects directly
   - Index cached at `~/.quiver/cache/index/acme.json`

3. **Install**: Creates a git worktree with sparse checkout for the specific skill
   - Each skill independently versioned

4. **Update**: `git fetch` on bare clone updates all refs
   - Then rebase only affected worktrees

## Creating a Registry

```bash
# 1. Create a new Git repo
mkdir my-skills && cd my-skills
git init

# 2. Add registry.yaml
cat > registry.yaml << 'EOF'
name: my-skills
description: My team's agent skills
settings:
  default-branch: main
EOF

# 3. Create a skill
qvr init skills/my-first-skill

# 4. Commit and push
git add . && git commit -m "Initial skills"
git remote add origin git@github.com:me/skills.git
git push -u origin main

# 5. Others can now add your registry
qvr registry add my-team git@github.com:me/skills.git
```
