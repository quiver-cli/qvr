---
name: trace-skill-activity
description: >
  Records and queries what agents actually did, attributed to the skill that was
  active, using qvr's experimental audit subsystem. Use when a user wants
  observability into agent or skill behavior — e.g. "track what my skills are
  doing", "audit agent tool calls", "which skill ran during this session", "show
  recent agent activity", or "export agent traces for analysis". Covers qvr audit
  enable, install-hooks, status, logs, sessions, and export. Experimental and
  opt-in; the command surface and storage may change.
metadata:
  author: quiver-playground
  version: "1.0.0"
---

# Trace skill activity with qvr audit

`qvr audit` captures an atomic trace of every tool, file op, and command an agent
runs — **attributed to the skill that was active** — into a local SQLite database
(`~/.quiver/skillops.db`). It answers "what did this agent actually do, and which
skill drove it?" The subsystem is **experimental, opt-in, and disabled by
default**; its command surface, storage format, and output shapes may change.

## When to use this

- The user wants visibility into agent tool/file/command activity.
- They want to attribute actions to the skill that was active.
- They want to export traces for external analysis, replay, or archival.

This is observability only — it does not change what skills are installed (that's
`onboard-skills`) or verify their integrity (that's `verify-skill-supply-chain`).

## How it works (two layers)

- **Raw traces** — the agent's own transcript lines and hook payloads, captured
  verbatim. This is the source of truth (`qvr audit export`, `sessions show`).
- **Derived spans** — Turn / Tool / Skill spans projected from the raw traces, as
  shown by `qvr audit logs`. A deriver must exist for the agent; without one the
  agent is raw-only and the derived views stay empty (the `DERIVES` column in
  `status` reports, per agent, whether a deriver exists).

## Workflow

### 1. Enable capture and wire agent hooks

`enable` sets `ops.enabled` in config and creates the database. `install-hooks`
adds a hook to each detected agent's config that pipes events into qvr (the
original config is backed up first under `$QUIVER_HOME/backups/<agent>/<ts>/`).

```
qvr audit enable
qvr audit install-hooks                 # wire every detected agent
qvr audit install-hooks --agent <agent>   # wire a single detected agent
qvr audit install-hooks --dry-run       # show planned changes, write nothing
```

### 2. Confirm it's recording

```
qvr audit status
```

Read the columns: detected, hooks installed/valid, `DERIVES` (raw-only vs derived
views available), `RECORDED` (individual events), `SESSIONS` (runs they group
into — normally several events per session), `ERRORS` (a non-zero value means
events reach qvr but fail to record), and last-event time.

### 3. Use your agent normally

With hooks wired and capture enabled, run the agent as usual. Tool calls, file
ops, and commands are recorded and attributed to the active skill.

### 4. Query activity

```
qvr audit sessions                                  # newest-first, with row counts
qvr audit sessions --agent <agent> --since 24h
qvr audit sessions show <session-id>                # one session's verbatim raw lines

qvr audit logs                                      # derived spans (default 50)
qvr audit logs --kind SKILL                         # only skill spans (or LLM / TOOL)
qvr audit logs --session <session-id> --limit 0     # everything for one session
```

### 5. Export for external analysis

`export` streams matching raw trace rows as JSONL (one object per line) — suitable
for archival, analysis, or replay:

```
qvr audit export > traces.jsonl
qvr audit export --session <session-id> -o session.jsonl
qvr audit export --source hook_payload              # or: transcript
```

### 6. Turn it off / unwire

```
qvr audit disable                       # stop the pipeline (hooks may still fire silently)
qvr audit uninstall-hooks               # remove hooks / restore from backup
qvr audit uninstall-hooks --agent <agent>
```

## Gotchas

- **Experimental.** Treat command names, DB schema, and output shapes as
  unstable; pin your qvr version if you script against them.
- **Nothing is captured until both steps run** — `enable` *and* `install-hooks`.
  `enable` alone creates the DB but no events flow; hooks alone fire but
  `disable` keeps them from recording.
- **`DERIVES=no` ⇒ empty derived views.** `logs` / spans / UI timeline will be
  blank for raw-only agents even though raw traces land — use `sessions show` /
  `export` to see the raw data.
- **`ERRORS > 0` in status** means hook payloads are arriving but failing to
  ingest — investigate before trusting the data.
- **Local only.** The database lives under `~/.quiver/`; nothing is sent anywhere.
  The read-only `qvr ui` dashboard can visualize sessions if you prefer a browser.

## Troubleshooting

- *No sessions after using the agent* — check `qvr audit status`: hooks installed
  and valid? capture enabled? If `ERRORS` is climbing, the hook is firing but
  ingest is failing.
- *`logs` is empty but `sessions` shows rows* — the agent is raw-only
  (`DERIVES=no`); query with `sessions show <id>` or `export` instead.
- *Want the verbatim transcript, not spans* — use `qvr audit sessions show <id>`
  or `qvr audit export`, which read raw traces rather than the derived view.
