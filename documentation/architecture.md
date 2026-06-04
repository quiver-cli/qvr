# Architecture

## Overview

qvr is a CLI-native agent skills manager that uses Git repositories as the storage and versioning backbone. Skills are installed via symlinks into agent directories, enabling zero-overhead reads and bidirectional sync.

## Core Design: Bare Clone + Worktrees + Sparse Checkout + Symlinks

### Why This Design

The naive approach (clone repo → checkout branch → symlink) has three fatal flaws:

1. **Branch checkout is global** — switching `code-review` to `v2` switches ALL skills in the registry
2. **Entire registry downloaded** — 200-skill registry downloads all 200 even if you need 3
3. **No two-way sync** — `git pull` can clobber local agent modifications

The solution uses Git's own primitives:

- **Bare clones** for registries (no working tree, minimal disk, one fetch updates all refs)
- **Worktrees** for each installed skill (independent branch per skill, no conflicts)
- **Sparse checkout** per worktree (only the skill directory, not the whole repo)
- **Symlinks** into agent directories (zero-copy, instant reads, modifications flow both ways)

### Storage Layout

```
~/.quiver/
├── registries/
│   ├── acme-labs/                       # Org parent
│   │   └── agent-skills.git/              # Bare clone (objects + refs only)
│   └── example-org/
│       └── skills.git/
│
├── worktrees/                             # SHA-keyed, shared across projects
│   ├── acme-labs/
│   │   └── agent-skills/
│   │       └── code-review/
│   │           └── abc1234/               # Sparse: only skills/code-review/
│   └── example-org/
│       └── skills/
│           └── test-runner/
│               └── def5678/
│
├── config.yaml
├── qvr.lock                               # Global ambient lock (--global lane)
└── cache/
    └── index/                             # Registry index cache (per-registry
                                           #   skill catalog, TTL'd, rebuilt from
                                           #   the bare clone — not skill files)

<project>/
├── qvr.lock                               # Project lock — source of truth for agents
└── .claude/skills/<skill>  -->            symlink into ~/.quiver/worktrees/.../<sha7>/
```

Single-skill repos live under the same `registries/` tree — `qvr registry add`
is the only entrypoint, so the indexer's job is to walk whatever's there
(one skill or many). Both `registries/` and `worktrees/` nest by
`<org>/<repo>` so the on-disk shape is uniform and a whole org can be wiped or
browsed at once.

### Source of truth vs. derived state

Only one thing on disk is authoritative; everything else is regenerable from it.
Keeping these distinct is why "cache" means two different things in Quiver — be
precise about which.

| Layer | Path | Role | Regenerable from |
|-------|------|------|------------------|
| **Bare clone** | `registries/<org>/<repo>.git/` | **Source of truth** — every object + ref. The network-expensive artifact. | upstream (re-clone) |
| **Registry index** | `cache/index/<name>.json` | Derived **catalog** of skills/versions a registry offers (names, descriptions, paths, refs). Powers discovery; holds no skill files. Persisted as a TTL **cache**. | the bare clone (re-index) |
| **Worktree store** | `worktrees/<org>/<repo>/<skill>/<sha7>/` | Derived, SHA-keyed **installs** — the materialized skill files the symlinks point at, shared across projects. | the lock + bare clone (`qvr sync`) |

Both derived layers are caches in the strict sense (rebuildable, disposable), which
is what lets `qvr cache clean` wipe them and lets `qvr remove` drop a shared worktree
without ref-counting — `qvr sync` rebuilds whatever a surviving project still needs.
The **registry index cache** answers "what skills exist?"; the **worktree store**
answers "which skills are installed, and what are their bytes?". `qvr cache prune`
GCs the latter (orphans that per-project `add`/`remove` structurally can't reclaim:
old SHAs left by `qvr switch`, or worktrees from a project deleted out-of-band).

### Data Flow

```
                REMOTE GIT REPO
                      │
           git fetch (bare clone)
                      │
              BARE CLONE (.git/)
                      │
         git worktree add --sparse
                      │
              WORKTREE (sparse)
              └── skills/code-review/
                      │
                   symlink
                      │
          ┌───────────┼───────────┐
          ▼           ▼           ▼
    .claude/skills  .cursor/rules  .codex/skills
    /code-review    /code-review   /code-review
```

## Performance Model

### Hot Path (Every Agent Invocation)

`qvr read code-review` or an agent reading `.claude/skills/code-review/SKILL.md`:
- Follow symlink → `fs.ReadFile()` → return content
- **Zero git operations, zero network I/O**
- Latency: microseconds

### Warm Path (Local-Only)

`qvr status`, `qvr list`:
- Read lock file (single TOML file) or run `git status` per worktree
- No network I/O
- Latency: milliseconds

### Cold Path (Network)

`qvr update`, `qvr add` / `qvr sync`:
- `git fetch` on bare clone (one fetch = all refs)
- Create worktree + sparse checkout (disk I/O)
- Only when explicitly requested
- Latency: seconds (network-bound)

## Bidirectional Sync

### Pull (upstream → local)

1. `git fetch` on bare repo
2. `git rebase origin/<branch>` in worktree
3. If conflict: abort rebase, flag to user
4. Symlinks unchanged (worktree path doesn't move)

### Push (local → upstream)

1. Agent modifies skill through symlink → change lands in worktree (it's a git repo)
2. `git add -A` + `git commit` + `git push` in the worktree
3. Lock file updated with new commit hash

## Module Dependencies

```
cmd/ (Cobra commands)
  → internal/config/    (Viper config)
  → internal/skill/     (business logic: loader, validator, linker,
                         installer, syncer, publisher)
  → internal/registry/  (registry manager, indexer, registry-index TTL cache)
  → internal/output/    (formatting — text/JSON printer)

internal/skill/
  → internal/git/       (git operations — go-git + shell-out)
  → internal/model/     (data types — Skill, Registry, LockFile, …)

internal/registry/
  → internal/git/
  → internal/model/

pkg/skillspec/           (public, no internal deps)

# Possible future areas:
#   internal/attestation/, internal/trust/  (signing + per-registry trust policy, v0.9)
#   internal/inventory/, internal/audit/    (cross-agent inventory + local audit log, v0.9)
#   internal/ui/ + ui/                      (embedded React dashboard, v0.9)
#   internal/doctor/                        (environment diagnostics, v1.0)
```
