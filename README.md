```
   ___        _
  / _ \ _   _(_)_   _____ _ __
 | | | | | | | \ \ / / _ \ '__|
 | |_| | |_| | |\ V /  __/ |
  \__\_\\__,_|_| \_/ \___|_|

  the git-native agent skills manager · CLI: qvr
```

> **Status: active development.** Project-local `qvr.lock` (schema v5), a shared
> SHA-keyed worktree store, strict visibility, supply-chain scan gates wired into
> install/sync/publish, and local audit views are available today. Public CLI
> behavior is intended to stay practical and git-native as the project moves
> toward v1.0.

---

## What

Quiver is to agent skills what `uv` is to Python packages: a CLI-native,
Git-native, zero-service way to install and manage [agent skills] across your
coding agents (Claude Code, Cursor, Copilot, Codex, Windsurf, anything that
reads skills from a directory).

- Registries are **plain Git repos**. No central server to run.
- Each project owns a **`qvr.lock`** — only skills explicitly added to that lock
  are visible to the agent. No ambient surprises.
- Skills install as **git worktrees + sparse checkout**, symlinked into each
  agent's skills directory. Independent per-skill versioning for free; two
  projects pinning the same SHA share one worktree.
- The read path is **just a symlink**: after `qvr add` (or `qvr add --local`),
  the agent opens `.claude/skills/<skill>/SKILL.md` directly through its own file
  access. No git, no network, no runtime, no CLI in the loop.
- Push your edits back upstream. Bidirectional sync, not just pull.

[agent skills]: https://agentskills.io

## Standards

Quiver builds on open standards rather than inventing its own formats — one for
what a skill *is*, one for what an agent *did*:

- **Skills follow [agentskills.io](https://agentskills.io/specification).** A
  skill is a directory with a `SKILL.md` whose YAML frontmatter carries `name`
  + `description` (plus optional `license`, `compatibility`, `allowed-tools`,
  `metadata`). Quiver's parser (`pkg/skillspec`) and validator enforce the spec;
  nothing Quiver-proprietary is required to author a skill.
- **Traces follow [OpenTelemetry](https://opentelemetry.io/docs/specs/semconv/gen-ai/).**
  `qvr audit` captures each agent's native transcript **verbatim** (lossless, the
  canonical source of truth), then *projects* it into OpenTelemetry spans —
  Turn / Tool / Skill — using the **GenAI semantic conventions** (`gen_ai.operation.name`,
  `gen_ai.request.model`, `gen_ai.usage.*`, `gen_ai.tool.*`). Spans serialize to
  the standard **OTLP** `resourceSpans` envelope, so `qvr audit spans --otlp`
  emits a payload any OTLP consumer (Jaeger, Tempo, Honeycomb, an OTel
  Collector) accepts unchanged.

The projection is regenerable: spans are derived from the raw bytes and stamped
with a `deriver_version`, so an improved deriver can be re-run over old captures
(`qvr audit rederive`) without ever re-capturing. Capture is normally hook-driven,
but `qvr audit ingest <transcript|rollout|dir>` records an already-produced
transcript with no live hook at all — the supported path for QA / CI / sandboxed
capture that must not mutate the developer's live agent config.

On top of those two standards sits one Quiver extension: the `skill.*` attribute
family. `skill.name` tags which skill a Turn/Tool span belongs to; when the span
can be tied to an installed skill, `skill.registry/version/commit/source/
subtree_hash` carry its lock-resolved identity, and `skill.verified` records
whether that identity was *proven* from the artifact the agent actually loaded
(vs. a name-keyed best guess). This makes skill attribution a first-class,
queryable dimension of the trace while staying valid OTLP; everything else is
stock OpenTelemetry.

**Skill-attributed, not generic logging.** Quiver is a skill manager, so its
traces are about *skills*, not a catch-all transcript log. A session is only
retained if it actually used a skill: when a session completes with no skill
usage, Quiver drops it whole (raw + spans), and `qvr audit gc` sweeps any that
slipped through. Skill usage is detected from each agent's **own** native
signal — Claude Code's `Skill` tool-call, and for Codex the model opening a
skill's `SKILL.md` per its injected `<skills_instructions>` — never by assuming
`qvr` is on the agent's PATH. (Sessions for an agent Quiver can't yet derive are
kept, since skill absence can't be proven there.)

## How it's wired

```
 ~/.quiver/                              shared worktree store + index cache
 |
 +-- config.yaml                         registered sources (URLs + names)
 +-- qvr.lock                            global ambient lock (--global lane)
 +-- registries/<org>/<repo>.git/                 bare clones (source of truth),
 |                                                nested by org
 +-- worktrees/<org>/<repo>/<skill>/<sha7>/       worktree store: immutable,
 |                                                SHA-keyed, shared across projects
 +-- cache/index/<name>.json                      registry index cache (TTL'd
                                                  catalog, rebuilt from the clone)


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

Tell `qvr` where skills live. Any git clone URL works — one skill or fifty,
doesn't matter. The indexer walks the repo and finds them.

```
    $ qvr registry add https://github.com/acme-labs/agent-skills
    registered as acme-labs/agent-skills

    $ qvr registry add https://github.com/user/my-skill
    registered as user/my-skill

    $ qvr registry list                              # all registered sources
    $ qvr registry list acme-labs/agent-skills    # skills inside one source
```

Use `--name <alias>` to override the auto-inferred `<org>/<repo>` name (handy
when two orgs publish a repo with the same name).

Accepted URL forms:

```
ok   https://github.com/org/repo
ok   https://github.com/org/repo.git
ok   git@github.com:org/repo.git
no   https://github.com/org/repo/tree/main/skills    <- web UI path
```

`tree/<branch>/<subdir>` is how github.com renders a folder in the browser; git
has no concept of cloning a subdirectory. Register the repo URL — the indexer
walks it.

## Installing a skill into a project

Two steps — register the source once, then add skills from it to as many
projects as you want.

```
$ qvr add tdd                            # writes ./qvr.lock, symlinks .claude/skills/tdd
$ qvr add tdd@v2                         # pin a branch or tag
$ qvr add --global diagnose              # ambient: appears in every Claude session
$ qvr sync                               # reconcile project against qvr.lock
$ qvr sync --strict                      # CI gate: fail on any subtree-hash drift
```

Anything under `.claude/skills/` that isn't in `qvr.lock` is hidden from the
agent on `qvr sync`. The lockfile is the only source of truth for what your
agent loads.

### Common `qvr add` flags

| Flag                 | What it does                                                                                          |
| -------------------- | ----------------------------------------------------------------------------------------------------- |
| `--target <agent>`   | Install into a specific agent dir (`claude`, `cursor`, `copilot`, …). Repeatable.                     |
| `--registry <name>`  | Scope resolution to one registered source. Disambiguates same-named skills across registries.         |
| `--as <local-name>`  | Install under a different lock-entry / symlink name so two versions of the same skill coexist (A/B). |
| `--frozen`           | Re-install from the lockfile entry. Refuses drift from the recorded subtree hash; no resolution.      |
| `--force`            | Replace an existing lock entry at a different ref. Use sparingly — `qvr switch` is usually cleaner.   |
| `--no-scan`          | Skip the security gate for this install only (see [Security scanning](#security-scanning)).           |
| `--global`           | Write to the user-global lock and symlink under `~/.<agent>/skills/` instead of the project.          |

#### A/B testing the same skill at two refs

```
$ qvr add code-review@v1.2.0
$ qvr add code-review@v1.3.0-rc1 --as code-review-rc
# both coexist; symlinks .claude/skills/code-review and .claude/skills/code-review-rc
```

`--as` is local-only — the lockfile records `canonical: code-review` so
`qvr upgrade`, `qvr publish`, etc. still resolve the alias back to its
registry-side skill.

`qvr link <local-path>` symlinks a local directory you're actively developing —
different from `qvr add`, which always goes through a registered source and a
pinned commit.

## Indexer

On `registry add` and `registry update`, the indexer reads the bare clone at
`HEAD` and builds a skills list. It accepts two layouts:

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
  `acme-labs/agent-skills`, `example-org/skills`, and friends.
- **Layout B** — no `skills/` dir, but a root `SKILL.md` → the repo is treated
  as one skill at `.`.

A skill with malformed frontmatter is silently skipped — one broken SKILL.md
doesn't fail the whole index build.

For each entry the index records: `name`, `description`, `metadata` from
frontmatter; the `path` inside the repo; and the full list of **branches and
tags**, which become the versions you can `qvr add <skill>@<ref>`.

### Registry index

The **registry index** is the catalog of what a registry offers — every skill's
`name`, `description`, `path`, and available refs — derived by walking the bare
clone at `HEAD`. It's the *source of truth* for discovery (`qvr search`,
`qvr add`), not a copy of any skill's files.

That catalog is persisted as a TTL **cache** at `~/.quiver/cache/index/<name>.json`,
written atomically (tmp + rename) so a concurrent reader never sees a half-written
file. The on-disk file is a cache of the index — the bare clone remains the source.

After **1 hour** the next read rebuilds the index from the local bare clone —
purely a local operation, no network. To pull new commits from upstream, run
`qvr registry update`; that fetches and rebuilds the index immediately. The
TTL is measured against the cache's embedded `generated` timestamp (set on
write), not the file's mtime, so manually touching the file won't expire it.

```
qvr search <q>              reads the index, builds if missing
qvr version list <skill>    reads the index
qvr registry add            builds on first clone
qvr registry update         rebuilds after fetch
qvr registry update --check refs only, no fetch, no index write
```

## Security scanning

Every install path runs the same scanner that powers `qvr scan` as a gate,
governed by three config keys (defaults in **bold**):

- `security.scan_on_install` (**true**) — master switch for the gate.
- `security.require_scan` (**false**) — when true, commands reject `--no-scan`
  instead of allowing a per-command bypass.
- `security.require_signed` (**false**) — when true, installs require a
  verified Git tag or commit signature.
- `security.block_severity` (**critical**) — findings at or above this
  severity refuse the operation. Set to `error`/`warning`/`info` to tighten,
  unset to disable blocking (findings still surface).

| When | What happens on a blocking finding |
|---|---|
| `qvr registry add <url>` | Every indexed skill is materialised in a temp worktree and scanned. Any blocking skill rolls back the whole registry add. |
| `qvr add <skill>` | Staged worktree is scanned before symlinks are created. Block → `installer.Remove` rolls back the install. |
| `qvr sync` | Each restored worktree is scanned; findings surfaced but **not** blocked — the lock already committed. Run `qvr remove <name>` or `qvr switch <name> <safer-ref>`. |
| `qvr publish [path]` | Local skill is scanned before the registry is touched; dry-run included. |

Every gated command takes `--no-scan` to bypass scan checks for that single
invocation unless `security.require_scan` is true. Set
`security.scan_on_install false` to disable scanning globally. Invalid Git
signatures always block; `security.require_signed true` also blocks unsigned
refs.

## Install

### Prebuilt binary (recommended)

A single binary with the dashboard (`qvr ui`) baked in — no Go or Node required.

**Linux / macOS:**

```
curl -fsSL https://raw.githubusercontent.com/quiver-cli/qvr/main/install.sh | sh
```

**Windows (PowerShell):**

```
irm https://raw.githubusercontent.com/quiver-cli/qvr/main/install.ps1 | iex
```

Pin a version with `QVR_VERSION=v0.12.0` (or `$env:QVR_VERSION`); override the
location with `QVR_INSTALL_DIR`.

### Updating

Once `qvr` is on your PATH, update it in place — no need to re-run the installer:

```
qvr upgrade            # download + verify + swap to the latest release
qvr upgrade --check    # just report whether a newer release exists
qvr upgrade --version v0.12.0   # pin a specific release
```

The release binary carries the dashboard embedded, so `qvr upgrade` brings the
UI (`qvr ui`) current too. The archive's checksum is verified before the binary
is atomically replaced. Works on Linux, macOS, and Windows with no `curl`/`tar`
dependency — the download, verify, and swap are all done in-process.

### From source

```
git clone https://github.com/quiver-cli/qvr.git
cd quiver
make build-all          # builds the React UI, then the binary (needs Node 20+)
make install            # -> /usr/local/bin/qvr  (sudo if needed)
```

Requires Go 1.22+ and Node 20+. Building from source is the only path that needs
a toolchain — `make build-all` embeds the dashboard so `qvr ui` works. (Plain
`go install` is intentionally not a supported install path: it can't run the npm
build, so it ships without the UI. Use the prebuilt binary above for the full
experience.)

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

A **ref is a ref**. Quiver treats branches, tags, and commit SHAs uniformly —
`qvr add foo@<anything-git-knows>` works. What differs is how you use each:

| Track                               | Best for                                            | Example                  |
| ----------------------------------- | --------------------------------------------------- | ------------------------ |
| **Tags** (`v1.2.0`)                 | Shared releases, other people installing your skill | `qvr add foo@v1.2.0`     |
| **Branches** (`main`, `experiment`) | Rolling dev tracks, local edit branches             | `qvr add foo@experiment` |
| **SHAs** (`abc1234`)                | CI-grade pins (lockfile does this for you)          | `qvr add foo@abc1234`    |

### Default resolution

Bare `qvr add foo` picks the **latest semver tag** when any exist (e.g.,
`v1.0.0` beats `v0.9.0`). If the registry has no semver tags, falls back to the
registry's default branch. Non-semver tags like `stable` or `latest` are ignored
for resolution — they tend to move and make "bare add" non-reproducible.

The lockfile (`qvr.lock`) always pins the **resolved commit SHA** regardless of
ref type, so reruns on another machine via `qvr sync` get the same bytes even if
the tag or branch shifts upstream.

### The edit → publish → release flow

```
    # pin to a specific release
    $ qvr add foo@v1.2.0

    # eject into the project so the skill becomes editable
    $ qvr edit foo                          # promotes .claude/skills/foo to a real dir
    $ vim .claude/skills/foo/SKILL.md
    $ git -C .claude/skills/foo commit -am "tighten review checklist"

    # push the commit back to the skill's origin (HEAD-only)
    $ qvr publish foo -m "tighten review checklist"

    # cut a new release on the same origin (auto un-ejects + repoints at the tag)
    $ qvr publish foo --tag v1.3.0 -m "v1.3.0"

    # teammate picks up the new release
    $ qvr registry update acme-labs/agent-skills
    $ qvr upgrade foo                       # resolves latest semver tag, moves worktree
```

Key properties:

- `qvr edit` is the eject: the symlink under `.claude/skills/<skill>` is
  replaced with a real directory containing a fresh git history, copied
  out of the shared worktree. Other agent targets (cursor, codex, …) get
  re-pointed to the canonical edit dir so they stay in sync.
- `qvr publish <skill>` (installed-skill mode) commits the eject dir into
  a staged clone of the skill's origin and pushes — never touches the
  remote until the local validate + scan gates pass.
- `qvr publish <skill> --tag v1.x.y` creates an annotated tag on the new
  commit, pushes branch+tag, then auto un-ejects: the lockfile flips out
  of edit mode and symlinks are repointed at the new tag's shared
  worktree. Same end state as `qvr remove --force && qvr add @<tag>` —
  no manual round-trip.
- `qvr publish <skill> --fork <git-url>` retargets the push to a new
  remote (your fork). Add `--migrate` to flip the lock entry's `Source`
  so future publishes track the fork and record
  `forkedFrom: <upstream>@<sha7>` in the lockfile entry. The published
  SKILL.md is byte-identical to the eject dir — qvr never stamps
  metadata into the artifact.
- `qvr publish ./path --registry <name>` (greenfield mode) clones the
  named registry, drops the local skill at `skills/<name>/`, commits,
  pushes — for adding a brand-new skill to a multi-skill registry.
- `qvr publish --dry-run` validates + scans + reports the target
  branch/tag without touching the remote.
- `qvr upgrade` follows the tag channel (latest semver); pass an explicit
  `<ref>` to repin elsewhere.
- `qvr switch <skill> <ref>` moves to any ref without branching — useful for
  quick version flips.

Two worktrees of the same skill at different SHAs coexist on disk (under
`~/.quiver/worktrees/<org>/<repo>/<skill>/<sha7>/`) — switching between them is
a symlink repoint, not a re-clone. Two projects pinning the same SHA share one
worktree.

## Inspection

Every read-mode command defaults to the project lock; pass `--global` to read
the ambient lock instead, or `--all` to union both (adds a SCOPE column where it
makes sense).

```
qvr list                    skills in the project lock
qvr list --all              project + global, with scope column
qvr info <skill>            structured details — frontmatter, refs, targets
qvr status [skill...]       per-skill modification state (clean / dirty / drift)
qvr outdated                per-registry ls-remote vs. pinned SHAs
qvr diff <skill>            local worktree changes against HEAD
qvr doctor                  diagnose broken installs, orphan artifacts,
                            unreferenced registries; --strict to fail on those
```

### Integrity

```
qvr lock verify             re-hash every entry, report drift / missing / unverified
qvr lock verify --frozen    exit non-zero on drift, missing worktree, or hash failure
qvr lock verify --strict    implies --frozen + also fails on unverified entries
qvr lock verify --repair    rewrite drifting Verification blocks from current disk
qvr lock upgrade            populate Verification blocks for any entries missing them
```

`--strict` is the CI gate: "every entry is verifiably the recorded state."
A missing worktree or a hash-computation failure flips it to exit 1, so the
gate can't silently pass when something's gone sideways.

### Worktree store

Installed skills materialize as SHA-keyed worktrees under
`~/.quiver/worktrees/`, shared across projects. The store is *derived* state —
`qvr sync` rebuilds any missing worktree from the lock + bare clone — so it can be
garbage-collected freely. `qvr cache` is that GC (the verbs mirror `uv cache`):

```
qvr cache list              reachable + orphan worktrees with sizes
qvr cache prune --dry-run   show what would be removed
qvr cache prune             delete worktrees no longer referenced by any project lock
qvr cache clean             wipe the whole store (and the registry index cache)
```

This is distinct from `qvr remove`, which is per-project (drops one lock entry +
its symlinks). The store is global, so orphans accumulate that no per-project
command can reclaim — an old SHA left behind by `qvr switch`, or worktrees from a
project you `rm -rf`'d. A worktree is reachable if any tracked project lock (or the
global lock) references it; `qvr add` and `qvr remove` keep the project list
(`~/.quiver/projects.json`) up to date automatically.

## Roadmap

Shipping today:

- **Foundation** — `init`, `validate`, `config`, `skillspec` parser
- **Registry** — `add`, `remove`, `list`, `update` (with `--check` dry-run),
  `search`, `version`, TTL-cached registry index
- **Project-local install** — `add` (with `--as`, `--frozen`, `--registry`),
  `sync` (with `--strict`), `link`, worktrees, symlinks, `pull`,
  `upgrade`/`switch`, AGENTS.md auto-sync,
  `doctor`/`info`/`list`/`status`/`diff`/`outdated`, `disable`/`enable`,
  private registries
- **Edit → publish flow** — `edit` (eject), `publish` with installed-skill
  mode (`--tag`, `--fork`, `--migrate`, `--auto-commit`,
  `--allow-lockfile-heal`) and greenfield path mode, auto un-eject after a
  tagged publish
- **Supply chain** — `scan` + scan gates on add/sync/publish, lock
  Verification blocks, `lock verify --frozen/--strict/--repair`,
  configurable severity thresholds
- **The uv loop** — reproducible resolve → lock → install. Installs are frozen
  immutable (`qvr edit` ejects a writable copy); `qvr sync` restores the
  *locked commit*, not whatever HEAD moved to (`--locked` for CI). Optional
  git-native provenance (`git verify-tag`/`verify-commit`) surfaced via
  `qvr provenance` and a `SIGNED` column — invalid signatures block, absent is
  fine unless `security.require_signed` is enabled. `qvr trust pin` and
  `qvr trust verify` enforce per-registry commit-author policy. `qvr tree`,
  `qvr export`/`import` for portability.
- **Worktree GC** — `cache list`/`cache prune`/`cache clean` over the worktree
  store with `projects.json` reachability tracking and orphan-cleanup hints;
  object dedup via hardlinked worktrees
- **Observability** — `qvr audit` captures agent transcripts verbatim and
  projects OpenTelemetry spans (`logs`, `spans`, `--otlp` export, `rederive`
  backfill) attributed to the active skill; embedded React dashboard via
  `qvr ui`. See [Standards](#standards).

Planned:

- `qvr add --local` vendor mode (materialize real files into the project),
  plus a `qvr publish` flow that round-trips vendored edits back upstream.
- v1.0 — prebuilt multi-platform binaries via Homebrew / curl installer,
  `qvr doctor`, shell completions, and faster parallel fetch/install.
- post-1.0 — two-stage scanner (`qvr scan --deep`, SBOM), `qvr inventory`, and
  OTLP push/live export to a configured collector.

Team workflows are deliberately delegated to git + your git host (GitHub
Teams, branch protection, CODEOWNERS); see
[team-workflows.md](documentation/guides/team-workflows.md).

## Documentation

In-depth docs live under [`documentation/`](documentation/):

- [Architecture](documentation/architecture.md) — storage layout, hot/warm/cold
  paths
- [Skill format](documentation/skill-format.md) — SKILL.md frontmatter and
  directory contract
- [Registry format](documentation/registry-format.md) — repo layouts the indexer
  accepts
- [Config reference](documentation/config-reference.md) —
  `~/.quiver/config.yaml` keys
- Guides: [getting started](documentation/guides/getting-started.md),
  [creating a skill](documentation/guides/creating-a-skill.md),
  [creating a registry](documentation/guides/creating-a-registry.md),
  [agent integration](documentation/guides/agent-integration.md),
  [team workflows](documentation/guides/team-workflows.md)

## Why another skills tool

Agent skills need the same operational loop developers expect from package
managers: resolve, lock, install, inspect, update, and publish. Skills also
benefit from normal Git workflows: branches for experiments, tags for releases,
forks for ownership changes, and review through your existing git host. Quiver
is the thin layer that makes Git feel like a package manager for agents.

## Develop

```
make fmt lint test build        # or: make all
go run . init test-skill
go run . validate testdata/valid-skill
```

Module path: `github.com/quiver-cli/qvr`. Module binary: `qvr`.

## License

MIT — see [LICENSE](LICENSE).

Quiver is built and maintained by [Rakshith S P](https://github.com/raks097).
Issues and pull requests welcome.
