```
   ___        _
  / _ \ _   _(_)_   _____ _ __
 | | | | | | | \ \ / / _ \ '__|
 | |_| | |_| | |\ V /  __/ |
  \__\_\\__,_|_| \_/ \___|_|

  the git-native agent skills manager · CLI: qvr
```

> **Status: pre-alpha.** Project-local lockfile, shared SHA-keyed cache,
> strict visibility — the v4 model below. Surface is stable enough to use;
> internals still moving.

---

## What

Quiver is to agent skills what `uv` is to Python packages: a CLI-native,
Git-native, zero-service way to install and manage [agent skills]
across your coding agents (Claude Code, Cursor, Copilot, Codex,
Windsurf, anything that reads skills from a directory).

- Registries are **plain Git repos**. No central server to run.
- Each project owns a **`qvr.lock`** — only skills explicitly added to
  that lock are visible to the agent. No ambient surprises.
- Skills install as **git worktrees + sparse checkout**, symlinked
  into each agent's skills directory. Independent per-skill
  versioning for free; two projects pinning the same SHA share one
  worktree.
- The hot path (`qvr read`) is **just follow a symlink**. No git,
  no network, no runtime.
- Push your edits back upstream. Bidirectional sync, not just pull.

[agent skills]: https://agentskills.io

## How it's wired

```
 ~/.quiver/                              shared cache, ambient toolbox
 |
 +-- config.yaml                         registered sources (URLs + names)
 +-- qvr.lock                            global ambient lock (--global lane)
 +-- registries/<name>.git/              bare clones — every URL lives here
 +-- worktrees/<reg>--<skill>--<sha7>/   immutable, SHA-keyed
 +-- cache/index/<name>.json             registry index TTL cache


 <project>/
 +-- qvr.lock                            project lock — source of truth
 +-- .claude/skills/<skill>      --> symlink into ~/.quiver/worktrees/
 +-- .cursor/rules/<skill>       --> symlink into ~/.quiver/worktrees/
 +-- .github/copilot/skills/...  --> symlink into ~/.quiver/worktrees/


      cold           warm            hot
    +-------+      +--------+     +-------+
    | fetch | ---> | status | --> | read  |
    +-------+      +--------+     +-------+
     network       local git      follow link
     on update     no network     zero ops
```

## Registering a source

Tell `qvr` where skills live. Any git clone URL works — one skill or
fifty, doesn't matter. The indexer walks the repo and finds them.

```
    $ qvr registry add https://github.com/vercel-labs/agent-skills
    registered as vercel-labs--agent-skills

    $ qvr registry add https://github.com/user/my-skill
    registered as user--my-skill

    $ qvr registry list                              # all registered sources
    $ qvr registry list vercel-labs--agent-skills    # skills inside one source
```

Use `--name <alias>` to override the auto-inferred `<org>--<repo>` name
(handy when two orgs publish a repo with the same name).

Accepted URL forms:

```
    ok   https://github.com/org/repo
    ok   https://github.com/org/repo.git
    ok   git@github.com:org/repo.git
    no   https://github.com/org/repo/tree/main/skills    <- web UI path
```

`tree/<branch>/<subdir>` is how github.com renders a folder in the browser;
git has no concept of cloning a subdirectory. Register the repo URL — the
indexer walks it.

## Installing a skill into a project

Two steps — register the source once, then add skills from it to as many
projects as you want.

```
    $ qvr add tdd                            # writes ./qvr.lock, symlinks .claude/skills/tdd
    $ qvr add tdd@v2                         # pin a branch or tag
    $ qvr add --global diagnose              # ambient: appears in every Claude session
    $ qvr sync                               # reconcile project against qvr.lock
```

Anything under `.claude/skills/` that isn't in `qvr.lock` is hidden from
the agent on `qvr sync`. The lockfile is the only source of truth for
what your agent loads.

`qvr link <local-path>` symlinks a local directory you're actively
developing — different from `qvr add`, which always goes through a
registered source and a pinned commit.

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

A skill with malformed frontmatter is silently skipped — one broken
SKILL.md doesn't fail the whole index build.

For each entry the index records: `name`, `description`, `metadata` from
frontmatter; the `path` inside the repo; and the full list of **branches
and tags**, which become the versions you can `qvr add <skill>@<ref>`.

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

    # register a source (bare clone, no working tree)
    $ qvr registry add git@github.com:acme/skills.git
    $ qvr registry list

    # find skills
    $ qvr search deploy
    $ qvr version list code-review

    # add a skill into the current project
    $ qvr add code-review
    $ qvr add code-review@v1.2.0
```

## Versioning skills

A **ref is a ref**. Quiver treats branches, tags, and commit SHAs
uniformly — `qvr add foo@<anything-git-knows>` works. What
differs is how you use each:

| Track | Best for | Example |
|---|---|---|
| **Tags** (`v1.2.0`)  | Shared releases, other people installing your skill | `qvr add foo@v1.2.0` |
| **Branches** (`main`, `experiment`) | Rolling dev tracks, local edit branches | `qvr add foo@experiment` |
| **SHAs** (`abc1234`) | CI-grade pins (lockfile does this for you) | `qvr add foo@abc1234` |

### Default resolution

Bare `qvr add foo` picks the **latest semver tag** when any
exist (e.g., `v1.0.0` beats `v0.9.0`). If the registry has no semver
tags, falls back to the registry's default branch. Non-semver tags
like `stable` or `latest` are ignored for resolution — they tend to
move and make "bare add" non-reproducible.

The lockfile (`qvr.lock`) always pins the **resolved commit SHA**
regardless of ref type, so reruns on another machine via `qvr sync`
get the same bytes even if the tag or branch shifts upstream.

### The edit → push → release flow

```
    # pin to a specific release
    $ qvr add foo@v1.2.0

    # branch off so edits don't touch upstream main
    $ qvr edit foo                 # creates qvr/<user>/foo locally
    $ vim .claude/skills/foo/SKILL.md
    $ qvr push foo                 # pushes the user branch upstream

    # cut a new release
    $ qvr publish .claude/skills/foo --tag v1.3.0

    # teammate picks up the new release
    $ qvr registry update vercel-labs--agent-skills
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
- `qvr upgrade` follows the tag channel (latest semver); pass an
  explicit `<ref>` to repin elsewhere.
- `qvr switch <skill> <ref>` moves to any ref without branching —
  useful for quick version flips.

Two worktrees of the same skill at different SHAs coexist on disk
(under `~/.quiver/worktrees/<reg>--<skill>--<sha7>/`) — switching
between them is a symlink repoint, not a re-clone. Two projects
pinning the same SHA share one worktree.

## Inspection

Every read-mode command defaults to the project lock; pass `--global` to
read the ambient lock instead, or `--all` to union both (adds a SCOPE
column where it makes sense).

```
    qvr list                    skills in the project lock
    qvr list --all              project + global, with scope column
    qvr info <skill>            structured details — frontmatter, refs, targets
    qvr outdated                per-registry ls-remote vs. pinned SHAs
    qvr diff <skill>            local worktree changes against HEAD
    qvr doctor                  diagnose broken installs, orphan artifacts,
                                unreferenced registries; --strict to fail on those
```

## Roadmap

Shipping today:

- **Foundation** — `init`, `validate`, `config`, `skillspec` parser
- **Registry** — `add`, `remove`, `list`, `update` (with `--check` dry-run),
  `search`, `version`, TTL-cached index
- **Project-local install** — `add`, `sync`, `link`, worktrees, symlinks,
  `pull`/`push`, `publish`, edit → push → release flow, `upgrade`/`switch`,
  AGENTS.md auto-sync, `doctor`/`info`/`list`/`diff`/`outdated`,
  `disable`/`enable`, private registries

Planned:

- `qvr cache prune` / `qvr cache list` with a `projects.json` reachability
  registry behind it.
- `qvr add --local` vendor mode (materialize real files into the project)
  + a `qvr publish` flow that can round-trip vendored edits back upstream.
- Teams: namespaces, forks, `TEAMS.yaml`.
- Local dashboard + prebuilt binary distribution.

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
