<div align="center">

<img src="assets/quiver-icon.svg" alt="Quiver" width="72" />

# quiver (`qvr`)

**The fast, governed way to ship agent skills.**

Install in milliseconds. Lock every byte. Scan every install. Trace every run.

</div>

<p align="center">
  <a href="https://github.com/astra-sh/qvr/releases"><img src="https://img.shields.io/github/v/release/astra-sh/qvr?color=a3e635" alt="Release" /></a>
  <a href="https://github.com/astra-sh/qvr/releases"><img src="https://img.shields.io/github/downloads/astra-sh/qvr/total.svg?color=a3e635" alt="Downloads" /></a>
  <a href="https://github.com/astra-sh/qvr/stargazers"><img src="https://img.shields.io/github/stars/astra-sh/qvr.svg?style=social" alt="GitHub stars" /></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-green.svg" alt="MIT License" /></a>
  <a href="https://github.com/astra-sh/qvr/actions/workflows/ci.yml"><img src="https://github.com/astra-sh/qvr/actions/workflows/ci.yml/badge.svg" alt="CI" /></a>
  <a href="go.mod"><img src="https://img.shields.io/badge/Go-%E2%89%A5%201.25-00ADD8.svg?logo=go&logoColor=white" alt="Go" /></a>
  <a href="https://goreportcard.com/report/github.com/astra-sh/qvr"><img src="https://goreportcard.com/badge/github.com/astra-sh/qvr" alt="Go Report Card"></a>
</p>

---

A skill is a folder of instructions and scripts your agent loads and executes on
your behalf. That makes it a **dependency** — with all the same questions `npm`
and `uv` taught us to ask: Where did it come from? What version is pinned? Has
it been scanned? Can I reproduce this exact set on another machine?

Today most skills are copy-pasted into agent directories by hand — unversioned,
unscanned, unattributable. Quiver is `uv` for agent skills: a Git-native,
zero-service CLI that gives skills the full package lifecycle —

```
resolve → lock → install (immutable) → scan → symlink → reproduce → observe
```

— across every coding agent that reads skills from a directory: Claude Code,
Cursor, Copilot, Codex, Gemini, and ~60 more.

---

## Fast by architecture, built for agents

`qvr` is a single Go binary on native git — no daemon, no service, no language
runtime anywhere near your agent. On a named-subset install from a real-world
registry, a cold (first-time) install lands in **~1.3s** and a warm (cached)
install in **~0.02s**:

<div align="center">
  <img src="assets/benchmark.svg" alt="Named-subset install benchmark — cold (first install) and warm (cached) wall-clock time" width="640" />
</div>

The speed isn't an optimization pass; it's the storage model:

- **One bare clone per registry, one sparse worktree per skill.** Installs are
  SHA-keyed and **immutable**, so content is shared by construction — two
  projects pinned to the same SHA share one copy on disk, and switching
  versions is a symlink repoint, not a re-clone.
- **The read path is a symlink.** When your agent loads a skill it follows a
  symlink and reads a file. Zero git operations, zero network, zero qvr — the
  tool gets out of the way the moment the install lands.
- **Made to be driven by agents, not just humans.** Every command supports
  `--output json`, keeps structured data on stdout and diagnostics on stderr,
  and exits with meaningful codes. `qvr` slots into an agent's tool loop as
  cleanly as into your shell.

```bash
qvr add code-review            # resolve, scan, lock, symlink — done
qvr add code-review@v1.2.0     # pin a tag, branch, or SHA
qvr sync                       # reproduce the locked set, byte-identical, anywhere
```

## Governed across projects, teams, and orgs

Quiver treats trust as a first-class output. A project's skill set lives in two
committed files: **`qvr.toml`** declares the intent, **`qvr.lock`** records the
resolved proof. Clone the repo on any machine, run `qvr sync`, and you get the
byte-identical, already-vetted skill set — not "whatever the registry serves
today."

```toml
# qvr.lock — one resolved, vetted entry per skill (machine-generated)
[[skill]]
name        = 'frontend-design'
registry    = 'anthropics/skills'
commit      = 'da20c92503b2e8ff1cf28ca81a0df4673debdbf7'    # resolved SHA
subtreeHash = 'sha256:21dce9699042…'                        # exact bytes installed
scan        = {reportSHA = 'sha256:8e861d1c…', decision = 'allowed', counts = {…}}
provenance  = {commitAuthor = 'Keith Lazuka <klazuka@anthropic.com>', signatureStatus = 'none'}
```

**Every install is scanned before it lands.** The built-in scanner runs a
15-category detection taxonomy over every skill at `qvr add`, `qvr publish`,
and on demand — prompt injection, data exfiltration, leaked secrets,
privilege escalation, MCP tool poisoning, supply-chain risks (including OSV
checks on declared dependencies), invisible-Unicode tricks, and more. Findings
gate the install, land in the lock as a verdict, and export as **SARIF** for
code-scanning pipelines.

**Everything is tracked, nothing is ambient.** Each lock entry records the
resolved commit, a subtree hash of the exact bytes, the scan verdict, and the
commit author — the lock *is* the audit trail. On `qvr sync`, anything in an
agent directory that isn't in the lock is hidden from the agent: the lockfile
is the only source of truth for what your agent loads.

**Policy travels and CI enforces it.**

```bash
qvr lock verify --strict    # CI gate: every entry verifiably matches disk
qvr sync --frozen           # CI gate: fail on any drift, change nothing
qvr trust pin               # per-registry commit-author allowlists
qvr provenance <skill>      # origin, signature status, author trust
qvr add --global <skill>    # ambient scope: governed the same way, machine-wide
```

Invalid signatures always block. Default agent targets are recorded in
`qvr.toml`, so routing policy is reviewed in the same PR as the skills
themselves — no machine-local drift between teammates.

## See what your skills actually do

Skills are software, so Quiver gives them the inspection surface software gets.
Agents already keep session history on disk; `qvr audit` reads those native
stores directly — **zero agent configuration**, and months of existing history
back-fill on the first scan.

Every capture is stored verbatim, then projected into **OpenTelemetry** spans
(Turn / Tool / Skill) using the GenAI semantic conventions. A `skill.*`
attribute family marks which skill each span belongs to — and whether that
identity was *proven* from the artifact the agent actually loaded. Pair proven
attribution with the lock's per-ref pinning and an A/B test stops being
anecdotal: each variant's spans trace back to a specific SHA.

```bash
qvr audit enable                    # opt in
qvr audit discover                  # scan agents' native session stores (incremental)
qvr audit sessions                  # recorded skill-using sessions
qvr audit logs                      # turn / tool / skill spans
qvr audit export > traces.jsonl     # OTLP-ready JSONL for Jaeger, Tempo, Honeycomb, …
```

The embedded dashboard — `qvr ui`, baked into the binary, no install — puts the
whole supply chain on one screen:

<div align="center">
  <img src="assets/dashboard.png" alt="qvr dashboard — skill report: version pin, provenance, utilization, token cost, per-agent and per-model breakdowns, and recent sessions" width="900" />
</div>

- **Skills & registries** — drill from a registry to a single skill: files,
  agent targets, scan results, version pins, and full version history.
- **Sessions & traces** — every recorded session, down to individual turn,
  tool, and skill spans.
- **Provenance** — where every installed byte came from, by author and
  signature status.
- **Dead weight** — skills that are installed but have never fired, and stale
  skills with no recent runs. Stop paying context for skills nobody uses.

> [!NOTE]
> Traceability is the foundation for *optimizing* skills: once every run is
> attributable to the exact skill bytes that produced it, you can tell which
> version actually moved the needle — and close the authoring loop on evidence
> instead of guesswork.

---

## Installation

### Prebuilt binary (recommended)

A single self-contained binary with the dashboard baked in — no Go or Node
required.

```bash
# Linux / macOS
curl -fsSL https://github.com/astra-sh/qvr/raw/main/install.sh | sh
```

```powershell
# Windows (PowerShell)
irm https://github.com/astra-sh/qvr/raw/main/install.ps1 | iex
```

The installer detects your OS/arch, downloads the matching release, verifies
its checksum, and drops `qvr` on your PATH. Tune it with `QVR_VERSION` (pin a
release) and `QVR_INSTALL_DIR` (install location). Then:

```bash
qvr --version
qvr doctor          # sanity-check the environment and any existing installs
```

Updating is in-place — download, checksum verify, and atomic swap, all
in-process:

```bash
qvr upgrade                     # latest release (brings the embedded UI current too)
qvr upgrade --check             # report whether a newer release exists
```

### From source

For contributors, or to build the latest `main`. Requires **Go 1.25+** and
**Node 20+**.

```bash
git clone https://github.com/astra-sh/qvr.git
cd qvr
make build          # builds the React UI, then embeds it into the binary
make install        # -> /usr/local/bin/qvr  (use sudo if needed)
```

> [!NOTE]
> Plain `go install` is intentionally **not** a supported path — it can't run
> the npm build, so it ships without the dashboard. Use the prebuilt binary or
> `make build`.

---

## Quick start

```bash
# register a source — any git clone URL, one skill or fifty
qvr registry add git@github.com:acme/skills.git
qvr search deploy

# add skills to the current project (scans, then writes qvr.toml + qvr.lock)
qvr add code-review
qvr add code-review@v1.2.0          # pin a tag, branch, or SHA

# commit qvr.toml + qvr.lock; teammates and CI reproduce with one command
qvr sync
```

---

## The full lifecycle

Quiver runs skills through a software lifecycle — and it **closes**. Authoring
a new version isn't a fresh start; it re-enters the same gate every consumer's
install went through.

```
source ─► registry add ─► scan ─► lint ─► add ─► edit ─► publish ─────┐
                          ▲                                           │
                          └──────────────── re-gate ◄─────────────────┘
```

### Consume

```bash
qvr registry add <git-url>        # index any git repo as a skill source
qvr add <skill>[@ref]             # scan, lock, symlink into every target agent
qvr add --global <skill>          # ambient: available in every session
qvr switch <skill> --latest       # follow the latest semver tag
qvr outdated                      # pinned SHAs vs. upstream tips
```

### Author

```bash
qvr init                          # bootstrap a project (writes qvr.toml)
qvr create my-skill               # scaffold a spec-valid skeleton
qvr lint my-skill                 # check against the agentskills.io spec
qvr edit code-review              # eject immutable install -> editable dir
qvr publish code-review --tag v1.3.0 -m "v1.3.0"   # re-gate, then push upstream
```

Version control is first-class because the lock pins **git refs**, not opaque
archives:

```bash
qvr add code-review@v1.3.0-rc1 --as code-review-rc   # two versions coexist for A/B
```

### Inspect & verify

```bash
qvr list                    # skills in the project lock (--all unions global)
qvr status [skill...]       # per-skill state: clean / dirty / drift
qvr scan <skill>            # run the security pipeline on demand (SARIF out)
qvr provenance <skill>      # origin, signature + author trust
qvr lock verify --strict    # CI gate: every entry is verifiably the recorded state
qvr doctor                  # broken installs, orphan artifacts (--strict to fail)
```

### Maintain

The SHA-keyed store is *derived* state — `qvr sync` rebuilds any missing
worktree from the lock — so it garbage-collects freely:

```bash
qvr cache list              # reachable + orphan worktrees with sizes
qvr cache prune             # drop worktrees no project lock references
qvr cache clean             # wipe the store and the registry index cache
```

---

## How it's wired

**Storage**: bare git clones → per-skill immutable worktrees (sparse checkout)
→ symlinks into agent dirs.

```
 ~/.quiver/                              shared store + index cache
 ├── config.yaml                         registered sources (URLs + names)
 ├── qvr.lock                            global ambient lock (--global lane)
 ├── registries/<org>/<repo>.git/        bare clones (source of truth)
 ├── worktrees/<org>/<repo>/<skill>/<sha7>/   immutable, SHA-keyed, shared
 └── cache/index/<name>.json             TTL'd registry index cache

 <project>/
 ├── qvr.toml                            declarative intent (skills + default targets)
 ├── qvr.lock                            resolved lock — source of truth
 ├── .claude/skills/<skill>   -> symlink into ~/.quiver/worktrees/
 ├── .agents/skills/<skill>   -> symlink into ~/.quiver/worktrees/
 └── .github/skills/<skill>   -> symlink into ~/.quiver/worktrees/
```

### Agent targets

Targets are a data-driven registry (~60 agents) compiled into the binary — run
`qvr target list` for the full set with directories and aliases. Paths are
sourced from each tool's official docs; many newer CLIs share the AGENTS.md
`.agents/skills` project location. Common core targets:

| Target   | Local dir          | Global dir                    | Aliases          |
| -------- | ------------------ | ----------------------------- | ---------------- |
| claude   | `.claude/skills`   | `~/.claude/skills`            | `claude-code`    |
| codex    | `.agents/skills`   | `~/.agents/skills`            |                  |
| cursor   | `.agents/skills`   | `~/.cursor/skills`            |                  |
| copilot  | `.github/skills`   | `~/.copilot/skills`           | `github-copilot` |
| gemini   | `.agents/skills`   | `~/.gemini/skills`            | `antigravity`    |
| hermes   | `.hermes/skills`   | `~/.hermes/skills`            | `hermes-agent`   |
| openclaw | `.agents/skills`   | `~/.openclaw/skills`          | `clawdbot`       |
| project  | `.agents/skills`   | `~/.agents/skills`            | `agents`         |

Pick which agents a project installs into with `qvr target add <name>...`; the
choice is recorded in `qvr.toml` so it travels with the repo. Selection order
is `--target` flag > `qvr.toml` default-targets > machine config.

---

## Standards

Quiver builds on open standards rather than inventing its own formats.

- **Skills follow [agentskills.io](https://agentskills.io/specification).** A
  skill is a directory with a `SKILL.md` whose YAML frontmatter carries `name` +
  `description` (plus optional `license`, `compatibility`, `allowed-tools`,
  `metadata`). Quiver's parser (`pkg/skillspec`) and linter enforce the spec —
  nothing Quiver-proprietary is required to author a skill.
- **Traces follow
  [OpenTelemetry](https://opentelemetry.io/docs/specs/semconv/gen-ai/).**
  Captures are stored verbatim and *projected* into OTLP spans, stamped with a
  `deriver_version` so an improved deriver can re-run over old captures
  (`qvr audit rederive`) without re-capturing.
- **Scan findings export as SARIF** for GitHub code scanning and any
  SARIF-aware pipeline.

---

## Documentation

In-depth docs live under [`documentation/`](documentation/):

- [Architecture](documentation/architecture.md) — storage layout, hot/warm/cold
  paths
- [Skill format](documentation/skill-format.md) — `SKILL.md` frontmatter and
  contract
- [Registry format](documentation/registry-format.md) — repo layouts the indexer
  accepts
- [Config reference](documentation/config-reference.md) —
  `~/.quiver/config.yaml` keys
- [Security scanning](documentation/security-scanning.md) — `qvr scan`, the
  install-time gate, and CI integration
- [Audit & tracing](documentation/audit-and-tracing.md) — `qvr audit` capture
  and OpenTelemetry spans
- Guides: [getting started](documentation/guides/getting-started.md) ·
  [creating a skill](documentation/guides/creating-a-skill.md) ·
  [creating a registry](documentation/guides/creating-a-registry.md) ·
  [agent integration](documentation/guides/agent-integration.md) ·
  [team workflows](documentation/guides/team-workflows.md)

---

<div align="center">

Built and maintained by [SRP](https://github.com/raks097) · MIT licensed ·
Issues and PRs welcome

</div>
