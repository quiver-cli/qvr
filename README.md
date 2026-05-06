```
   ___        _
  / _ \ _   _(_)_   _____ _ __
 | | | | | | | \ \ / / _ \ '__|
 | |_| | |_| | |\ V /  __/ |
  \__\_\\__,_|_| \_/ \___|_|

  the git-native agent skills manager · CLI: qvr
```

> **Status: pre-alpha (v0.4.4).** What ships today: scaffold, validate,
> config, registry (TTL-cached index + `update --check` dry-run), search,
> version, add, install, pull/push, publish, edit → push → release flow,
> upgrade/switch, AGENTS.md auto-sync, disable/enable, doctor, info, ls,
> diff, outdated. Expect breakage.

---

## What

Quiver is to agent skills what `uv` is to Python packages: a CLI-native,
Git-native, zero-service way to install and manage [agent skills]
across your coding agents (Claude Code, Cursor, Copilot, Codex,
Windsurf, anything that reads skills from a directory).

- Registries are **plain Git repos**. No central server to run.
- Skills install as **git worktrees + sparse checkout**, symlinked
  into each agent's skills directory. Independent per-skill
  versioning for free.
- The hot path (`qvr read`) is **just follow a symlink**. No git,
  no network, no runtime.
- Push your edits back upstream. Bidirectional sync, not just pull.

[agent skills]: https://agentskills.io

## How it's wired

```
 ~/.quiver/
 |
 +-- registries/<name>.git/       bare clones (no working tree)
 |                                  |
 |                                  v
 +-- worktrees/<reg>--<skill>--<branch>/     per-skill worktree
 |   |                             |   |       sparse checkout
 |   |                             |   |
 |   v                             v   v
 |  .claude/skills/<skill> ---> symlink
 |  .cursor/rules/<skill>  ---> symlink
 |  .github/copilot/skills ---> symlink
 |
 +-- standalone/<slug>/           single-skill repos
 +-- cache/index/<name>.json      cached registry indexes
 +-- config.yaml


      cold           warm            hot
    +-------+      +--------+     +-------+
    | fetch | ---> | status | --> | read  |
    +-------+      +--------+     +-------+
     network       local git      follow link
     on update     no network     zero ops
```

## Registries

Two ways to pull skills onto your machine. Pick by shape, not by preference.

### Multi-skill registry — `qvr registry add <name> <url>`

A registry is a plain Git repo that gets bare-cloned to
`~/.quiver/registries/<name>.git/`. Skills inside it are **discovered by
the indexer** — you don't list them anywhere.

```
    $ qvr registry add vercel https://github.com/vercel-labs/agent-skills
    $ qvr registry list                  # all configured registries
    $ qvr registry list vercel           # skills in one registry
    $ qvr registry list vercel anthropic # skills across several
```

The URL must be a **git clone URL**, not a GitHub web path:

```
    ok   https://github.com/org/repo
    ok   https://github.com/org/repo.git
    ok   git@github.com:org/repo.git
    no   https://github.com/org/repo/tree/main/skills   <- web UI path
```

`/tree/<branch>/<subdir>` is how github.com renders a folder in the browser;
git has no concept of cloning a subdirectory. If the skills you want live
inside a subfolder, just clone the whole repo — the indexer walks it.

### Single-skill repo — `qvr add <url>`

For one-offs that ship as their own repo with a root-level `SKILL.md`.
Clones into `~/.quiver/standalone/<slug>/`.

```
    $ qvr add https://github.com/user/my-skill
```

## Indexer

On `registry add` and `registry update`, the indexer reads the bare clone
at `HEAD` and builds a skills list. It accepts two layouts:

```
    layout A (multi-skill)              layout B (single skill)
    repo/                               repo/
      skills/                             SKILL.md
        code-review/SKILL.md              references/
        deploy/SKILL.md                   scripts/
        ...
```

- **Layout A** — every immediate subdirectory of `skills/` with a valid
  `SKILL.md` becomes one indexed skill. This is the convention used by
  `vercel-labs/agent-skills`, `anthropics/skills`, and friends.
- **Layout B** — no `skills/` dir, but a root `SKILL.md` → the repo is
  treated as one skill at `.`.

A skill with malformed frontmatter is silently skipped — one broken SKILL.md
doesn't fail the whole index build.

For each entry the index records: `name`, `description`, `metadata` from
frontmatter; the `path` inside the repo; and the full list of **branches
and tags**, which become the versions you can `install <skill>@<ref>`.

### Index cache

The index is persisted at `~/.quiver/cache/index/<name>.json`, TTL **1 hour**.
Written atomically (tmp + rename) so a concurrent reader never sees a
half-written file.

```
    qvr search <q>              reads cache, builds if missing
    qvr version list <skill>    reads cache
    qvr registry add            builds on first clone
    qvr registry update         rebuilds after fetch
    qvr registry update --check refs only, no fetch, no cache write
```

## Install

Prebuilt binaries are still in flight. For now, build from source:

```
    git clone https://github.com/raks097/quiver.git
    cd quiver
    make build              # -> bin/qvr
    make install            # -> /usr/local/bin/qvr  (sudo if needed)
```

Or via Go directly:

```
    go install github.com/raks097/quiver@latest
```

Requires Go 1.22+.

## Quick start

```
    # scaffold + validate a skill
    $ qvr init my-skill
    $ qvr validate my-skill

    # add a registry (bare clone, no working tree)
    $ qvr registry add team git@github.com:acme/skills.git
    $ qvr registry list

    # find skills
    $ qvr search deploy
    $ qvr version list code-review

    # onboard a single-skill repo
    $ qvr add github.com/user/my-skill
```

## Versioning skills

A **ref is a ref**. Quiver treats branches, tags, and commit SHAs
uniformly — `qvr install foo@<anything-git-knows>` works. What
differs is how you use each:

| Track | Best for | Example |
|---|---|---|
| **Tags** (`v1.2.0`)  | Shared releases, other people installing your skill | `qvr install foo@v1.2.0` |
| **Branches** (`main`, `experiment`) | Rolling dev tracks, local edit branches | `qvr install foo@experiment` |
| **SHAs** (`abc1234`) | CI-grade pins (lockfile does this for you) | `qvr install foo@abc1234` |

### Default resolution

Bare `qvr install foo` picks the **latest semver tag** when any
exist (e.g., `v1.0.0` beats `v0.9.0`). If the registry has no semver
tags, falls back to the registry's default branch. Non-semver tags
like `stable` or `latest` are ignored for resolution — they tend to
move and make "bare install" non-reproducible.

The lockfile (`qvr.lock.json`) always pins the **commit SHA**
regardless of ref type, so reruns on another machine get the same
bytes even if the tag or branch shifts upstream.

### The edit → push → release flow

```
    # pin to a specific release
    $ qvr install foo@v1.2.0

    # branch off so edits don't touch upstream main
    $ qvr edit foo                 # creates qvr/<user>/foo locally
    $ vim .claude/skills/foo/SKILL.md
    $ qvr push foo                 # pushes the user branch upstream

    # cut a new release
    $ qvr publish .claude/skills/foo --tag v1.3.0

    # teammate picks up the new release
    $ qvr registry update team
    $ qvr upgrade foo              # resolves latest semver tag, moves worktree
```

Key properties:

- `qvr edit` forks onto a user-owned branch; upstream stays
  untouched until you explicitly push.
- `qvr push` warns when the worktree still tracks the registry's
  default branch, so a casual push doesn't land on someone else's
  `main` by accident.
- `qvr publish --tag` creates an annotated tag on the new commit
  and pushes both.
- `qvr upgrade` follows the tag channel (latest semver); pass
  `--to <ref>` to pin a specific ref.
- `qvr switch <skill> <ref>` moves to any ref without branching —
  useful for quick version flips.

Two worktrees of the same skill at different refs coexist on disk
(under `~/.quiver/worktrees/<reg>--<skill>--<ref>/`) — switching
between them is a symlink repoint, not a re-clone.

## Roadmap

Shipping today:

- **Foundation** — `init`, `validate`, `config`, `skillspec` parser
- **Registry** — `add`, `remove`, `list`, `update` (with `--check` dry-run),
  `search`, `version`, TTL-cached index
- **Install / publish** — worktrees, symlinks, `pull`/`push`, `publish`,
  edit → push → release flow, `upgrade`/`switch`, AGENTS.md auto-sync,
  `doctor`/`info`/`ls`/`diff`/`outdated`, `disable`/`enable`, private
  registries

Planned:

- Teams: namespaces, forks, `TEAMS.yaml`
- Local dashboard + prebuilt binary distribution

## Documentation

In-depth docs live under [`documentation/`](documentation/):

- [Architecture](documentation/architecture.md) — storage layout, hot/warm/cold paths
- [Skill format](documentation/skill-format.md) — SKILL.md frontmatter and directory contract
- [Registry format](documentation/registry-format.md) — repo layouts the indexer accepts
- [Config reference](documentation/config-reference.md) — `~/.quiver/config.yaml` keys
- Guides: [getting started](documentation/guides/getting-started.md),
  [creating a skill](documentation/guides/creating-a-skill.md),
  [creating a registry](documentation/guides/creating-a-registry.md),
  [agent integration](documentation/guides/agent-integration.md),
  [team workflows](documentation/guides/team-workflows.md)

## Why another skills tool

Existing options are either proprietary (vendor-locked catalogs)
or ad-hoc (copy-paste + dotfiles). Skills move fast — you want a
branch-per-experiment workflow, the ability to fork a teammate's
skill, and to push your fixes back. Git already has all of that.
Quiver is the thin layer that makes Git feel like a package
manager for agents.

## Develop

```
    make fmt lint test build        # or: make all
    go run . init test-skill
    go run . validate testdata/valid-skill
```

Module path: `github.com/raks097/quiver`. Module binary: `qvr`.

## License

MIT — see [LICENSE](LICENSE).

Quiver is built and maintained by [Rakshith S P](https://github.com/raks097).
Issues and pull requests welcome.
