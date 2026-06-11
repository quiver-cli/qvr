---
name: trace-skill-activity
description: >
  Records and queries what agents actually did, attributed to the skill that was
  active, using qvr's experimental audit subsystem. Use when a user wants
  observability into agent or skill behavior — e.g. "track what my skills are
  doing", "audit agent tool calls", "which skill ran during this session", "show
  recent agent activity", or "export agent traces for analysis". Covers qvr audit
  enable, discover, status, logs, sessions, and export. Experimental and
  opt-in; the command surface and storage may change.
metadata:
  author: quiver-playground
  version: "2.0.0"
---

# Trace skill activity with qvr audit

`qvr audit` records what your agents actually did — every turn, tool call, and
command — **attributed to the skill that was active** — into a local SQLite
database (`~/.quiver/skillops.db`). Agents already keep their own session
history on disk; qvr reads those native stores directly, so there is **no agent
configuration to touch** and months of existing history back-fill on the first
scan. The subsystem is **experimental, opt-in, and disabled by default**; its
command surface, storage format, and output shapes may change.

## When to use this

- The user wants visibility into agent tool/file/command activity.
- They want to attribute actions to the skill that was active.
- They want to export traces for external analysis, replay, or archival.

This is observability only — it does not change what skills are installed (that's
`onboard-skills`) or verify their integrity (that's `verify-skill-supply-chain`).

## How it works (two layers)

- **Raw traces** — the agent's own transcript lines, captured verbatim. This is
  the source of truth (`qvr audit export`, `sessions show`).
- **Derived projection** — the unified per-session model (title, model,
  turn/tool counts, skills) plus Turn / Tool / Skill spans projected from the
  raw traces, as shown by `qvr audit sessions` and `qvr audit logs`. A deriver
  must exist for the agent (the `DERIVES` column in `status` reports this);
  only deriver-backed agents are scanned.

## Workflow

### 1. Enable capture and discover your history

`enable` sets `ops.enabled` in config and creates the database. `discover`
scans every supported agent's native session store and records the
skill-using sessions it finds; sessions that provably used no skill are
counted but not stored (pass `--keep-all` to import everything).

```
qvr audit enable
qvr audit discover                      # scan every agent's session store
qvr audit discover --agent <agent>      # scan a single agent
qvr audit discover --since 90d          # bound the back-fill window
qvr audit discover --dry-run            # report what would be scanned
```

Scans are incremental: re-running over an unchanged store costs almost
nothing, so run `discover` again whenever you want fresh sessions picked up.
`qvr ui` also scans on launch and keeps rescanning while it runs, so the
dashboard tracks new sessions live (`--no-discover` turns this off).

### 2. Confirm what's recorded

```
qvr audit status
```

Read the columns: `DERIVES` (whether qvr can project this agent's format),
`RECORDED` (raw rows), `SESSIONS` (the runs they group into), and last-event
time.

### 3. Query activity

```
qvr audit sessions                                  # newest-first, titled, with skills
qvr audit sessions --agent <agent> --since 24h
qvr audit sessions show <session-id>                # one session's verbatim raw lines

qvr audit logs                                      # derived spans (default 50)
qvr audit logs --kind SKILL                         # only skill spans (or LLM / TOOL)
qvr audit logs --session <session-id> --limit 0     # everything for one session
```

### 4. Export for external analysis

`export` streams matching raw trace rows as JSONL (one object per line) — suitable
for archival, analysis, or replay:

```
qvr audit export > traces.jsonl
qvr audit export --session <session-id> -o session.jsonl
```

### 5. Turn it off

```
qvr audit disable                       # stop recording; the database stays
```

## Gotchas

- **Experimental.** Treat command names, DB schema, and output shapes as
  unstable; pin your qvr version if you script against them.
- **Skill-less sessions are not stored** by default — discover counts them (the
  dashboard's activity panel shows the split) but keeps only skill-attributed
  evidence. Use `--keep-all` if you want everything.
- **`DERIVES=no` ⇒ not scanned.** An agent without a deriver is listed in
  `status` but its store is not ingested.
- **Local only.** The database lives under `~/.quiver/`; nothing is sent
  anywhere. The `qvr ui` dashboard visualizes sessions and activity analytics
  if you prefer a browser.

## Troubleshooting

- *No sessions after running discover* — check `qvr audit status` and the
  discover report: `SEEN=0` means no store was found for that agent on this
  machine; `SKIPPED` counts sessions that used no skill (not stored by design).
- *A session is missing* — it likely used no skill. Re-run with
  `qvr audit discover --keep-all` to import everything.
- *Want the verbatim transcript, not spans* — use `qvr audit sessions show <id>`
  or `qvr audit export`, which read raw traces rather than the derived view.
