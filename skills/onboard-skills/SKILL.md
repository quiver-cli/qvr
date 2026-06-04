---
name: onboard-skills
description: >
  Discovers and installs agent skills into a project (or the user-global lane)
  with the qvr CLI, treating qvr.lock as the single source of truth. Use when a
  user wants to find, add, register, or install skills from a skills registry or
  GitHub repo with qvr — e.g. "register a skill registry", "search for a qvr
  skill", "qvr add this skill", "install a skill globally", or "why is my skill
  not loading after I dropped it into the agent's skills directory". Covers registry add, search,
  the one-step add github.com/org/repo/skill form, --global, and sync.
metadata:
  author: quiver-playground
  version: "1.0.0"
---

# Onboard skills with qvr

`qvr` installs **agent skills** (SKILL.md bundles) into a project from one or
more Git **registries**. qvr is agent-agnostic — it installs into whichever agent
target directories you configure (e.g. `.claude/skills/`, `.cursor/rules/`, …).
The lockfile `qvr.lock` (TOML, schema v5) is the only source of truth for what an
agent loads — anything that lands in a managed agent directory without a matching
lock entry is hidden on the next sync. This skill walks discovery → install →
reconcile.

## When to use this

- The user wants to find a skill, add a registry, or install a skill with `qvr`.
- A skill was copied into a managed agent directory by hand and "isn't showing up".
- The user wants a skill available in **every** session (ambient/global), not
  just one project.

Do **not** use this for editing/forking a skill (see `fork-and-publish-skill`),
or for reproducing an existing set on another machine (see `reproduce-skill-env`).

## Prerequisites

1. Confirm the CLI is present and note the version (workflows below assume
   0.10.x):

   ```
   qvr --version
   ```

2. Run from the project root where you want `qvr.lock` to live. The lockfile and
   the agent-target symlinks are written relative to the current directory.

## Workflow

### 1. Discover what's available

Search already-registered registries (substring match on name/description, with
optional hard filters). At least one of a query, `--tag`, or `--author` is
required:

```
qvr search pdf
qvr search --tag testing --author acme
qvr search review --full          # untruncated descriptions
qvr search forms --github         # search GitHub for repos tagged agent-skills
```

List registries and the skills each one indexes:

```
qvr registry list
qvr registry list anthropics/skills --full
```

### 2. Install a skill

**One-step install (no prior registry add needed).** Point at a skill inside a
repo; qvr auto-registers the source, then installs:

```
qvr add github.com/org/repo/tdd            # register org/repo, install tdd
qvr add github.com/org/repo/tdd@v2         # pin a branch or tag
qvr add github.com/org/repo                # single-skill repo: install the lone skill
```

**By name, once the source is registered:**

```
qvr registry add https://github.com/acme-labs/agent-skills
qvr add tdd
qvr add tdd lint review                    # batch — each must resolve to a registered skill
qvr add tdd@v2                             # @<ref> pins a branch, tag, or commit
```

Useful flags on `add`:

- `--registry <org>/<repo>` — scope resolution to one registry when the same
  skill name exists in several. **Use the full `<org>/<repo>` name**, not a short
  alias.
- `--target <agent>[,<agent>…]` — install into specific agent target dirs
  (e.g. `--target claude,cursor`); defaults to your configured `default_target`.
- `--as <localname>` — install under a different local name so two versions of
  the same skill can coexist (single skill only).
- `--force` — allow replacing an existing lock entry pinned at a different ref.
- `--no-scan` — skip the install-time security scan (see `verify-skill-supply-chain`
  before reaching for this).

### 3. Install into the global (ambient) lane

A globally installed skill appears in every session via `~/.<agent>/skills/`,
backed by `~/.quiver/qvr.lock`:

```
qvr add --global diagnose
qvr list --global
```

### 4. Reconcile on-disk state to the lock

`qvr sync` makes the managed agent target dirs match the lock:
missing worktrees are restored, and managed symlinks **not** in the lock are
removed — this is the "hidden by default" guarantee that makes the lockfile
authoritative.

```
qvr sync                 # reconcile project
qvr sync --global        # reconcile the global lane
qvr sync --dry-run       # show what would change, touch nothing
```

A symlink whose target points outside qvr's managed cache (e.g. into your own
dev dir) is left alone and surfaced in the output rather than deleted.

### 5. Confirm the result

```
qvr list                 # installed skills from the lock
qvr tree                 # grouped by registry / target
qvr info tdd             # metadata, provenance, file tree (local-only, fast)
```

## Gotchas

- **The lockfile wins.** Dropping a folder into a managed agent directory does nothing
  durable — it's removed on the next `qvr sync` unless there's a lock entry.
  Install via `qvr add` (or `qvr link` for a local path) instead.
- **Registry names are nested `<org>/<repo>`.** The bare clone lives at
  `~/.quiver/registries/<org>/<repo>.git/`. `--registry`, `registry update`, and
  `registry remove` all expect that full name. Override the inferred name with
  `qvr registry add <url> --name <alias>` only when two repos collide.
- **Web tree/blob URLs are rejected.** Pass the repo URL, not a
  `github.com/org/repo/tree/<ref>/<path>` link — git can't clone a subdirectory.
- **Skills resolve under two layouts:** nested `skills/<name>/` and flat-root
  `<name>/` at the repo top level. Both work; you don't choose.

## Troubleshooting

- *"skill not found" on add* — the source isn't registered, or the name is
  ambiguous. Run `qvr registry list` / `qvr search <name>`, then scope with
  `--registry <org>/<repo>`.
- *Installed but the agent doesn't see it* — run `qvr sync`, then `qvr doctor` to
  surface broken installs, stale symlinks, or orphaned worktrees.
- *Same name in two registries* — disambiguate with `--registry`, or install one
  under `--as <localname>`.
