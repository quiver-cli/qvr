# Team Workflows

> **Status: planned.** First-class team commands (`qvr fork`, `qvr team`)
> are not yet shipping — `qvr diff` already exists for inspecting local
> worktree changes. Use registries + branches in the meantime.

## Multi-Team Setup

### Organization with multiple teams

```bash
# Shared org-wide registry
qvr registry add org git@github.com:acme/org-skills.git

# Team-specific registries
qvr registry add platform git@github.com:acme/platform-skills.git
qvr registry add ml-ops git@github.com:acme/ml-skills.git
```

### Namespaced skills

Skills from different registries are namespaced to avoid collision:

```bash
qvr add acme/code-review          # From org registry
qvr add platform/deploy-helper    # From platform registry
qvr add ml-ops/model-deploy       # From ML ops registry
```

## Forking Skills

Customize a shared skill for your team:

```bash
# Fork from org registry to your team's registry
qvr fork acme/code-review --to platform

# The fork has forked-from metadata
# Now modify it for your team's needs
qvr push code-review -m "customized for platform team"
```

## Comparing Versions

```bash
# Diff between branches
qvr diff code-review main v2

# Diff between fork and original
qvr diff platform/code-review acme/code-review
```

## Bidirectional Sync Workflow

### Scenario: Agent improves a skill

1. Agent modifies skill during work session (through symlink)
2. You review changes: `qvr status`
3. Push improvements: `qvr push code-review -m "agent-improved patterns"`
4. Team benefits from the improvement

### Scenario: Upstream changes

1. Teammate pushes skill update
2. Check for updates: `qvr pull --check`
3. Pull changes: `qvr pull code-review`
4. If conflict: resolve in worktree, then `qvr push`

### Scenario: Version switch

1. New version available on `v2` branch
2. Switch: `qvr switch code-review v2`
3. Test with your agent
4. If issues: `qvr switch code-review main` (rollback)

## Team Management

### TEAMS.yaml

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
```

### Commands

```bash
# View teams
qvr team list --registry org

# Add a member
qvr team add platform carol --registry org

# Remove a member
qvr team remove platform bob --registry org
```

Team changes are committed to the registry repo (auditable via git log).

## CI/CD Integration

### Validate skills in CI

```yaml
# .github/workflows/validate-skills.yml
name: Validate Skills
on: [pull_request]
jobs:
  validate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: |
          go install github.com/quiver-sh/qvr@latest
          qvr validate skills/ --output json
          qvr scan skills/ --format json
```

### Auto-publish on merge

```yaml
# .github/workflows/publish-skills.yml
name: Publish Skills
on:
  push:
    branches: [main]
    paths: ['skills/**']
jobs:
  publish:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: |
          go install github.com/quiver-sh/qvr@latest
          qvr validate skills/
          qvr scan skills/
```

## Best Practices

1. **One registry per team** — keeps permissions simple
2. **Shared org registry** for cross-team skills
3. **Use branches for versions** — `main` is latest stable, `v1`/`v2` for pinned versions
4. **Tag releases** — `v1.0.0` for reproducible installs
5. **Require scan in CI** — catch issues before they reach agents
6. **Fork, don't copy** — `forked-from` metadata enables tracking upstream changes
7. **Review skill changes** like code — PRs, reviews, CI checks
