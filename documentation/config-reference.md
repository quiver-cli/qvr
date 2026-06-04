# Configuration Reference

qvr stores configuration at `~/.quiver/config.yaml`. Override with `QUIVER_HOME`
env var.

## Config File

```yaml
# Registries
registries:
  community:
    url: https://github.com/quiver-sh/community-skills.git
  acme:
    url: git@github.com:acme/skills.git

# Defaults
default_target: claude # Default agent target
default_registry: acme # Default registry for publish

# GitHub integration
github_token: "" # For GitHub search + higher rate limits

# Security
security:
  scan_on_install: true # Auto-scan skills on install
  require_scan: false # Forbid --no-scan when true
  require_signed: false # Require verified Git signatures for installs
  block_severity: critical # Block install at this severity (critical, error, warning)

# Output
output:
  format: text # text | json
  color: auto # auto | always | never
```

## Config Keys

| Key                        | Type   | Default    | Description                         |
| -------------------------- | ------ | ---------- | ----------------------------------- |
| `registries.<name>.url`    | string | -          | Git URL for a registry              |
| `default_target`           | string | `claude`   | Default `--target` for install/link |
| `default_registry`         | string | -          | Default `--registry` for publish    |
| `github_token`             | string | `""`       | GitHub API token for search         |
| `security.scan_on_install` | bool   | `true`     | Auto-scan on install                |
| `security.require_scan`    | bool   | `false`    | Reject `--no-scan` bypasses         |
| `security.require_signed`  | bool   | `false`    | Require verified Git signatures     |
| `security.block_severity`  | string | `critical` | Minimum severity to block install   |
| `output.format`            | string | `text`     | Default output format               |
| `output.color`             | string | `auto`     | Color mode                          |

## CLI Commands

```bash
# Get a value
qvr config get default_target
# → claude

# Set a value
qvr config set default_target cursor
qvr config set security.scan_on_install false
qvr config set security.require_scan true
qvr config set security.require_signed true
qvr config set github_token ghp_xxxxx

# Get all (dump config)
qvr config get
qvr config get --output json
```

Pin trusted commit authors per registry. A skill installs only when the commit
that **last touched its own subtree** (`skills/<name>`) — not whoever pushed
last to the branch — was authored by a pinned identity:

```bash
# Pin by email (matches any "Name <email>" with that address) …
qvr trust pin acme/skills alice@example.com
# … or by full identity.
qvr trust pin acme/skills "Alice <alice@example.com>"

qvr trust list                         # pinned authors per registry
qvr trust verify                       # check installed skills against pins
qvr trust unpin acme/skills alice@example.com  # remove one author
qvr trust unpin acme/skills            # remove all pins for the registry
```

A pin must carry an email (a bare GitHub handle is rejected, since it can never
match a git commit identity).

Skills may also declare the signer their ref must carry, via
`metadata.signed_by` in `SKILL.md`. When the resolved ref has a verified Git
signature whose signer does not match the declaration, the install is blocked:

```yaml
---
name: code-review
description: …
metadata:
  signed_by: alice@example.com
---
```

## Environment Variables

All config keys can be overridden via environment variables with `QUIVER_`
prefix:

```bash
export QUIVER_DEFAULT_TARGET=cursor
export QUIVER_GITHUB_TOKEN=ghp_xxxxx
export QUIVER_SECURITY_SCAN_ON_INSTALL=false
export QUIVER_OUTPUT_FORMAT=json
```

## Precedence (highest to lowest)

1. CLI flags (`--output json`, `--target claude`)
2. Environment variables (`QUIVER_*`)
3. Config file (`~/.quiver/config.yaml`)
4. Defaults

## Storage Paths

| Path                                               | Purpose                                                                              |
| -------------------------------------------------- | ------------------------------------------------------------------------------------ |
| `~/.quiver/config.yaml`                            | Configuration                                                                        |
| `~/.quiver/qvr.lock`                               | Global ambient lock (`--global` lane)                                                |
| `~/.quiver/registries/<org>/<repo>.git/`           | Bare clones of every registered source                                               |
| `~/.quiver/worktrees/<org>/<repo>/<skill>/<sha7>/` | SHA-keyed sparse worktrees, shared across projects (mirrors the `registries/` shape) |
| `~/.quiver/cache/index/<name>.json`                | Cached registry indexes                                                              |
| `<project>/qvr.lock`                               | Project lock — source of truth for what agents see                                   |

Override base path: `export QUIVER_HOME=/custom/path`
