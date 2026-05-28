# Configuration Reference

qvr stores configuration at `~/.quiver/config.yaml`. Override with `QUIVER_HOME` env var.

## Config File

```yaml
# Registries
registries:
  community:
    url: https://github.com/quiver-sh/community-skills.git
  acme:
    url: git@github.com:acme/skills.git

# Defaults
default_target: claude               # Default agent target
default_registry: acme               # Default registry for publish

# GitHub integration
github_token: ""                     # For GitHub search + higher rate limits

# Security
security:
  scan_on_install: true              # Auto-scan skills on install
  block_severity: critical           # Block install at this severity (critical, error, warning)

# Output
output:
  format: text                       # text | json
  color: auto                        # auto | always | never
```

## Config Keys

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `registries.<name>.url` | string | - | Git URL for a registry |
| `default_target` | string | `claude` | Default `--target` for install/link |
| `default_registry` | string | - | Default `--registry` for publish |
| `github_token` | string | `""` | GitHub API token for search |
| `security.scan_on_install` | bool | `true` | Auto-scan on install |
| `security.block_severity` | string | `critical` | Minimum severity to block install |
| `output.format` | string | `text` | Default output format |
| `output.color` | string | `auto` | Color mode |

## CLI Commands

```bash
# Get a value
qvr config get default_target
# → claude

# Set a value
qvr config set default_target cursor
qvr config set security.scan_on_install false
qvr config set github_token ghp_xxxxx

# Get all (dump config)
qvr config get
qvr config get --output json
```

## Environment Variables

All config keys can be overridden via environment variables with `QUIVER_` prefix:

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

| Path | Purpose |
|------|---------|
| `~/.quiver/config.yaml` | Configuration |
| `~/.quiver/qvr.lock` | Global ambient lock (`--global` lane) |
| `~/.quiver/registries/<name>.git/` | Bare clones of every registered source |
| `~/.quiver/worktrees/<reg>--<skill>--<sha7>/` | SHA-keyed sparse worktrees, shared across projects |
| `~/.quiver/cache/index/<name>.json` | Cached registry indexes |
| `<project>/qvr.lock` | Project lock — source of truth for what agents see |

Override base path: `export QUIVER_HOME=/custom/path`
