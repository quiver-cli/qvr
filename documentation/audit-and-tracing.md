# Audit & Tracing

> **Experimental and opt-in.** The `qvr audit` subsystem is disabled by default.
> Its command surface, storage schema, and output shapes may change — pin your
> `qvr` version if you script against them.

You can't optimize what you can't measure. `qvr audit` records what your agents
actually did — every turn, tool call, and command — **attributed to the skill
that was active** — so you can evaluate and improve skills on evidence rather than
guesswork. Everything stays local: capture lands in a SQLite database at
`~/.quiver/skillops.db`, and nothing is sent anywhere.

Agents are the capture infrastructure: each one already persists its own session
history on disk (Claude Code's `~/.claude/projects`, Codex's rollout files, …).
`qvr audit discover` reads those native stores directly — **no agent
configuration is ever touched**, and months of existing history back-fill on the
first scan.

## Two layers: raw traces and a derived projection

- **Raw traces** — the agent's own transcript lines, captured **verbatim**. This
  is the lossless source of truth (`qvr audit export`, `qvr audit sessions show`).
- **Derived projection** — the unified per-session model (title, model, turn /
  tool counts, skills used) plus Turn / Tool / Skill spans, *projected* from the
  raw traces using the OpenTelemetry
  [GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/),
  shown by `qvr audit sessions` and `qvr audit logs`. A `skill.*` attribute
  family tags which skill each span belongs to, resolved against `qvr.lock`
  (`skill.verified` marks identity proven by the loaded path).

Because the projection is derived, an improved deriver re-runs over old captures
without re-capturing (`qvr audit rederive`). Derivers ship for **claude, codex,
copilot, cursor, gemini, droid, pi, hermes, and openclaw**; an agent without a
deriver is listed but inert until one ships.

## Workflow

### 1. Enable capture and discover your history

```bash
qvr audit enable                       # opt in; creates the database
qvr audit discover                     # scan every agent's session store
qvr audit discover --agent claude      # scan a single agent
qvr audit discover --since 90d         # bound the back-fill window
qvr audit discover --dry-run           # report what would be scanned
```

Scans are incremental and idempotent: a stat ledger remembers every file seen,
so re-running over an unchanged store costs almost nothing. Sessions that
provably used **no** skill are counted but not stored (qvr keeps
skill-attributed evidence, not generic transcripts); pass `--keep-all` to import
everything.

Re-run `discover` whenever you want fresh sessions picked up — or just open
the dashboard: `qvr ui` scans on launch and keeps rescanning in the background
while it runs, so new sessions appear live (`--no-discover` turns the scans
off; the discover button forces one on demand).

### 2. Confirm what's recorded

```bash
qvr audit status
```

Read the columns: `DERIVES` (whether qvr can project this agent's format),
`RECORDED` (raw rows), `SESSIONS`, and last-event time.

### 3. Query activity

```bash
qvr audit sessions                                  # newest-first, titled, with skills
qvr audit sessions --agent claude --since 24h
qvr audit sessions show <session-id>                # one session's verbatim raw lines

qvr audit logs                                      # derived spans (default 50)
qvr audit logs --kind SKILL                         # only skill spans (or LLM / TOOL)
qvr audit logs --session <session-id> --limit 0     # everything for one session
```

### 4. Export for external analysis

`export` streams matching raw trace rows as JSONL (one object per line) —
suitable for archival, analysis, or replay, and OTLP-ready for any OpenTelemetry
consumer (Jaeger, Tempo, Honeycomb, an OTel Collector):

```bash
qvr audit export > traces.jsonl
qvr audit export --session <session-id> -o session.jsonl
```

### 5. Turn it off

```bash
qvr audit disable                        # stop recording; the database stays
```

## The dashboard

The `qvr ui` dashboard (embedded in the binary, served at
`http://127.0.0.1:7878`) visualizes recorded sessions and activity analytics —
sessions over time split by agent and skill usage, the by-skill breakdown, the
skill report card and dead-weight view — alongside a skill's files, targets,
scan results, version history, and provenance.

```bash
qvr ui                   # live-scans session stores while running (--no-discover to skip)
```

## Notes

- **Local only.** The database lives under `~/.quiver/`; nothing leaves the machine.
- **No agent configuration is modified.** Discovery only reads the session files
  agents already write. (Earlier qvr versions installed hooks into agent
  configs; that mechanism is gone — if you ran `install-hooks` on an old
  version, remove the `qvr _hook` entries from your agents' hook configs.)
- **`DERIVES=no` ⇒ not scanned.** Discovery only ingests agents whose format qvr
  can derive, so the skill-retention gate stays provable.
- Low-level plumbing verbs (`gc`, `ingest`, `raw`, `rederive`, `spans`) exist for
  maintenance and re-derivation but are hidden — prefer the verbs above.

See the [trace-skill-activity](../skills/trace-skill-activity/SKILL.md) skill for
a task-oriented walkthrough, and [config-reference.md](config-reference.md) for
the `ops.enabled` gate.
