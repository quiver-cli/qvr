# Team Workflows

Quiver is deliberately thin on team primitives. Git and your git host (GitHub, GitLab, …) already own team membership, permissions, fork/diff, and review. Quiver builds on top of that surface instead of duplicating it.

## What Quiver does and doesn't do for teams

| You need to… | Quiver-native | Use this instead |
|---|---|---|
| Add/remove team members | — | GitHub Teams (or your host's equivalent). |
| Gate who can merge to a registry | — | Branch protection + CODEOWNERS. |
| Fork a skill into your own registry | — | `gh repo fork` or `git clone && push to a new remote`. Import a single skill subdirectory when you only want one skill. |
| Diff two versions of a skill | partial (`qvr diff` shows local worktree edits) | `git diff <v1>..<v2> -- skills/<name>/` inside the registry for version-to-version. |
| Use the same skill across multiple teams | ✓ | One registry per team plus a shared org registry. Install from any of them. |
| Pin a skill to a specific version or branch | ✓ | `qvr add acme/code-review@v2.1.0` (any git ref). |
| Track upstream changes after a fork | ✓ | `qvr publish --fork --migrate` records `forkedFrom: <upstream>@<sha>` in the lockfile entry. The published SKILL.md stays byte-identical. |
| Audit who's installed what across machines | ✓ (planned) | `qvr inventory` remains a post-v1 fleet feature. |
| Require a registry's skills be signed | ✓ | `qvr config set security.require_signed true`, then pin expected commit authors with `qvr trust pin <registry> <author>`. |

If you find yourself wanting a `qvr team`/`qvr fork` command, the answer is almost always "use the git tool you'd reach for outside Quiver." That keeps a single source of truth (the git host) and avoids parallel state.

## Multi-team setup

### Organization with multiple teams

```bash
# Each registry's name is inferred as <org>/<repo>
qvr registry add git@github.com:acme/org-skills.git       # -> acme/org-skills
qvr registry add git@github.com:acme/platform-skills.git  # -> acme/platform-skills
qvr registry add git@github.com:acme/ml-skills.git        # -> acme/ml-skills
```

Permissions are enforced by the git host (who can push to `acme/platform-skills`). Quiver reads everything; it writes nothing back without you explicitly running `qvr publish`.

### Namespaced skill references

The registry's `<org>/<repo>` name is its namespace; scope a skill with `--registry`:

```bash
qvr add code-review --registry acme/org-skills
qvr add deploy-helper --registry acme/platform-skills
qvr add model-deploy --registry acme/ml-skills
```

When a bare name (`qvr add code-review`) could resolve to multiple registries, Quiver should fail with a chooser instead of silently picking one. (Tracked as issue #106 — getting tightened up in the v1.0 close-out.)

## Forking a skill

Quiver doesn't ship a `qvr fork` command. Two paths, depending on whether you're forking from outside Quiver or from an already-installed skill.

**From outside Quiver — straight git:**

```bash
# 1. Fork the upstream registry (or clone + push to a new remote you own).
gh repo fork acme/org-skills --clone --remote

# 2. Import just the skill you want into your team's registry.
mkdir -p platform-skills/skills/code-review
git -C org-skills archive HEAD:skills/code-review | tar -x -C platform-skills/skills/code-review
cd platform-skills
git add skills/code-review && git commit -m "fork code-review from acme/org-skills"
git push
```

**From an installed skill — `qvr publish --fork --migrate`:**

```bash
qvr edit code-review
# ...make your changes...
qvr publish code-review --fork git@github.com:acme/platform-skills.git --migrate --tag v0.1.0
```

After `--migrate`, the lockfile entry records `forkedFrom: <original-upstream>@<sha>` so the provenance chain is preserved locally. The published SKILL.md is byte-identical to your eject dir — qvr never stamps metadata into the artifact.

## Comparing versions

`qvr diff <skill>` shows uncommitted edits in the local worktree — useful before `qvr publish`. For version-to-version comparison, use git directly inside the registry clone:

```bash
# Local worktree edits before push
qvr diff code-review

# Diff between two branches/tags inside one registry
cd ~/.quiver/registries/acme/org-skills.git
git diff main..v2 -- skills/code-review/

# Diff between a fork and its origin (after both are cloned locally)
diff -ruN \
  ~/.quiver/registries/acme/org-skills.git/skills/code-review/ \
  ~/.quiver/registries/acme/platform-skills.git/skills/code-review/
```

## Bidirectional sync workflow

### Agent improves a skill

1. Agent modifies the skill during a work session (through the symlink — `qvr` doesn't get involved).
2. Review: `qvr status` (and `qvr diff <skill>` for the line-level changes).
3. Eject + publish: `qvr edit code-review` → `qvr publish code-review -m "agent-improved patterns"` (re-runs the lint + scan gate before pushing).
4. Team benefits from the improvement.

### Upstream changes

1. Teammate pushes a skill update.
2. Check: `qvr outdated`.
3. Fast-forward: `qvr switch code-review --tip` (or `qvr pull` for every skill).
4. Resolve any conflicts in the worktree, then `qvr edit` + `qvr publish`.

### Version switch

1. New version available on `v2`.
2. Switch: `qvr switch code-review v2`.
3. Test with your agent.
4. Rollback if needed: `qvr switch code-review main`.

## CI / CD integration

### Lint skills in CI

```yaml
# .github/workflows/lint-skills.yml
name: Lint Skills
on: [pull_request]
jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: |
          curl -fsSL https://github.com/astra-sh/qvr/raw/main/install.sh | sh
          qvr lint skills/ --output json
          qvr scan skills/ --format sarif > scan.sarif
```

### Gate a consuming repo on its lockfile

A repo that *installs* skills commits `qvr.toml` + `qvr.lock`; CI asserts the
checkout still matches the resolved, vetted set:

```yaml
# .github/workflows/verify-skills.yml
name: Verify Skills
on: [pull_request]
jobs:
  verify:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: |
          curl -fsSL https://github.com/astra-sh/qvr/raw/main/install.sh | sh
          qvr sync --locked        # restore from the lock; fail if it would change
          qvr lock verify --strict # fail on any drift from the recorded bytes
          qvr trust verify         # registry commit-author policy
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
          curl -fsSL https://github.com/astra-sh/qvr/raw/main/install.sh | sh
          qvr lint skills/
          qvr scan skills/
```

## Best practices

1. **One registry per team** — keeps git permissions simple. Cross-team skills live in a shared `org` registry.
2. **Use branches for versions** — `main` is latest stable; `v1`/`v2` for pinned majors.
3. **Tag releases** — `v1.0.0` makes installs reproducible (`qvr add foo@v1.0.0`).
4. **Require scan and trust in CI** — catch issues before they reach agents. Use `qvr lock verify --strict` for integrity and `qvr trust verify` for registry commit-author policy.
5. **Treat skills like code** — PRs, reviews, CI checks. Your git host already does this; let it.
