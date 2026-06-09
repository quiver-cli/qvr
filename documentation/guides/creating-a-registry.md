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
skills-dir: skills        # where the indexer looks for skills (default: skills)
ignore: []                # optional path globs the indexer must skip
settings:
  require-scan: true
  default-branch: main
EOF

# 3. Create your first skill: scaffold OUTSIDE the repo, then vendor the
#    files in without the scaffold's .git
(cd /tmp && qvr create my-first-skill --standalone)
mkdir -p skills
cp -R /tmp/my-first-skill skills/ && rm -rf skills/my-first-skill/.git

# 4. Commit and push
git add .
git commit -m "Initial registry with first skill"
git remote add origin git@github.com:your-org/skills.git
git push -u origin main
```

> **Why scaffold outside?** `qvr create --standalone` initializes its own git
> repo. Nesting that inside the registry's work tree makes `git add` record a
> *gitlink* — a pointer commit with none of the skill's files — so the pushed
> registry indexes 0 skills. qvr refuses in-worktree standalone creates for
> this reason; the copy + `rm -rf .git` step keeps the files, not the pointer.

## Registry Structure

```
my-team-skills/
├── registry.yaml         # Recommended: metadata + indexer scoping (skills-dir, ignore)
├── skills/               # All skills live here
│   ├── code-review/
│   │   └── SKILL.md
│   ├── deploy-helper/
│   │   ├── SKILL.md
│   │   ├── scripts/
│   │   └── references/
│   └── test-runner/
│       └── SKILL.md
└── README.md             # Optional: documentation
```

When `registry.yaml` is present, the indexer scopes discovery to `skills-dir`
(default `skills/`, plus a root-level `SKILL.md`) and honors the `ignore`
globs. Without it, the whole tree is walked. In both modes, skill directories
under `testdata/` or `fixtures/` are always excluded — scanner fixtures and
test data never reach `qvr search`/`qvr add`.

## Adding Skills to a Registry

### Option 1: Direct commit

```bash
# Scaffold outside the registry repo, vendor the files in (see "Why
# scaffold outside?" above), then commit
(cd /tmp && qvr create new-skill --standalone)
cp -R /tmp/new-skill my-team-skills/skills/ && rm -rf my-team-skills/skills/new-skill/.git
cd my-team-skills
# Edit skills/new-skill/SKILL.md
git add skills/new-skill
git commit -m "Add new-skill"
git push
```

### Option 2: Via qvr publish

```bash
# From a local skill directory (use the full <org>/<repo> registry name)
qvr publish ./my-standalone-skill --registry your-org/skills
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

Quiver doesn't define its own team file. Use your git host's existing team primitives — GitHub Teams + branch protection + CODEOWNERS are the recommended pattern. See [team-workflows.md](team-workflows.md) for the full story.

## Access Control

qvr delegates auth to Git. Use your Git hosting platform's features:

- **GitHub**: Branch protection rules, CODEOWNERS file, team permissions
- **GitLab**: Protected branches, merge request approvals
- **SSH keys**: For team members to clone/push

No additional auth system needed.

## Publishing Your Registry

Tell your team to add it (the name is inferred as `<org>/<repo>`):

```bash
qvr registry add git@github.com:your-org/skills.git     # -> your-org/skills
```

For public registries, anyone with the URL can add it:

```bash
qvr registry add https://github.com/org/public-skills.git   # -> org/public-skills
```
