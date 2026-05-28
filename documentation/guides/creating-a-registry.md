# Creating a Registry

A registry is a Git repository that contains skills. Anyone with access to the repo can install skills from it.

## Quick Setup

```bash
# 1. Create repo
mkdir my-team-skills && cd my-team-skills
git init

# 2. Add registry metadata
cat > registry.yaml << 'EOF'
name: my-team-skills
description: My team's curated agent skills
maintainers:
  - name: Your Name
    github: your-github-username
settings:
  require-scan: true
  default-branch: main
EOF

# 3. Create your first skill
mkdir -p skills
qvr init skills/my-first-skill

# 4. Commit and push
git add .
git commit -m "Initial registry with first skill"
git remote add origin git@github.com:your-org/skills.git
git push -u origin main
```

## Registry Structure

```
my-team-skills/
├── registry.yaml         # Required: registry metadata
├── skills/               # All skills live here
│   ├── code-review/
│   │   └── SKILL.md
│   ├── deploy-helper/
│   │   ├── SKILL.md
│   │   ├── scripts/
│   │   └── references/
│   └── test-runner/
│       └── SKILL.md
├── TEAMS.yaml            # Optional: team definitions
└── README.md             # Optional: documentation
```

## Adding Skills to a Registry

### Option 1: Direct commit

```bash
cd my-team-skills
qvr init skills/new-skill
# Edit skills/new-skill/SKILL.md
git add skills/new-skill
git commit -m "Add new-skill"
git push
```

### Option 2: Via qvr publish

```bash
# From a standalone skill directory
cd my-standalone-skill
qvr publish --registry my-team
# This copies the skill into the registry, commits, and pushes
```

### Option 3: Pull request workflow

Team members create PRs to add/modify skills. Maintainers review and merge.

## Versioning with Branches

Create branches for different versions:

```bash
# Current development
git checkout main

# Stable release
git checkout -b v1
git tag v1.0.0
git push origin v1 --tags

# Next version
git checkout -b v2
# ... make changes ...
git push origin v2
```

Users install specific versions:
```bash
qvr add code-review@v1      # Branch
qvr add code-review@v1.0.0  # Tag
qvr add code-review         # Default branch (main)
```

## Team Collaboration

Add a TEAMS.yaml for team visibility:

```yaml
teams:
  platform:
    description: Platform engineering
    members:
      - github: alice
        role: maintainer
      - github: bob
        role: contributor
    skills:
      - deploy-helper
      - infra-scanner

  frontend:
    description: Frontend team
    members:
      - github: carol
        role: maintainer
    skills:
      - react-patterns
      - a11y-checker
```

## Access Control

qvr delegates auth to Git. Use your Git hosting platform's features:

- **GitHub**: Branch protection rules, CODEOWNERS file, team permissions
- **GitLab**: Protected branches, merge request approvals
- **SSH keys**: For team members to clone/push

No additional auth system needed.

## Publishing Your Registry

Tell your team to add it:

```bash
qvr registry add team-skills git@github.com:your-org/skills.git
```

For public registries, anyone with the URL can add it:

```bash
qvr registry add community https://github.com/org/public-skills.git
```
