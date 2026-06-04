---
name: reproduce-skill-env
description: >
  Reproduces an exact agent-skill set across machines, teammates, and CI using
  qvr's portable manifest and lockfile guarantees. Use when a user wants to share,
  pin, replicate, or CI-gate their qvr skills — e.g. "export my skills", "import
  this skill manifest", "pin everything to exact commits", "make skills
  reproducible", "fail CI if skills drift", or "onboard a teammate to the same
  skills". Covers qvr export/import, --frozen pinning, and the sync --locked /
  sync --check CI assertions.
metadata:
  author: quiver-playground
  version: "1.0.0"
---

# Reproduce a skill environment with qvr

qvr separates *intent* from *bytes*: `qvr.lock` pins each skill to a `commit` and
a `subtreeHash`, so the exact content can be restored even if upstream refs move.
On top of that, `qvr export` emits a small, human-readable manifest that a
teammate (or a CI job) can `qvr import` to rebuild the same set from scratch —
without any pre-existing registry configuration. This skill covers sharing a set,
pinning it hard, and asserting reproducibility in CI.

## When to use this

- The user wants to share their skill set with a teammate or another machine.
- They want byte-for-byte reproducibility (pin to exact commits, not floating
  refs).
- They want CI to **fail** if the checked-out skills don't match the lockfile.

For first-time discovery/install of individual skills, use `onboard-skills`
instead.

## Key concepts

- **Manifest (export)** — a 3-column text file: repo URL, skill name, version.
  Portable and reviewable. By default it records the ref; `--frozen` records the
  exact pinned commit.
- **Lockfile (`qvr.lock`)** — the authoritative TOML v5 record with commit +
  subtreeHash. Commit it to version control.
- **`mode: edit` / `mode: link` skills** live in the project filesystem and are
  not portable across projects; they're skipped from exports by default.

## Workflow

### 1. Export the current set

```
qvr export > skills.txt                          # ref-only (floating)
qvr export --frozen > skills.lock.txt            # pin to recorded commits
qvr export --output-file=skills.txt --include-aliases
qvr export --include-local                        # emit edit/link skills as commented docs
```

Choose `--frozen` when you want the recipient to get the *exact same bytes*; omit
it when you want them to track whatever the ref currently points at.

### 2. Import on another machine / for a teammate

`qvr import` registers each source repo if needed, then installs each skill via
the same code path as `qvr add`:

```
qvr import skills.txt                             # register sources + install each
qvr import skills.lock.txt                        # frozen manifest: per-line --commit= pins are honored — plain import, no qvr.lock needed
qvr import --target=claude,cursor skills.txt      # install into specific targets
```

The byte-exact pin lives in the manifest's `--commit=<sha>` field (written by
`export --frozen`), so a plain `qvr import` on a fresh machine already reproduces
exact bytes — you do **not** pass `--frozen` here. `import --frozen` is a
stricter *re-import* guard: it refuses subtreeHash drift on `--commit`-pinned
entries and therefore requires an existing `qvr.lock` to compare against (it
errors `--frozen requires a readable lock file` on a from-scratch machine).

Per-line failures surface as `✗ import …: <reason>` on stderr; the command exits
non-zero if anything failed, but successful installs are kept. A URL already
registered under a different alias is reused silently.

### 3. Commit the lockfile

The manifest bootstraps a fresh machine; the **lockfile** is what guarantees
identical bytes thereafter. Commit `qvr.lock` (and the manifest, if you keep one)
to version control:

```
git add qvr.lock skills.lock.txt
git commit -m "Pin agent skill set"
```

### 4. Assert reproducibility in CI

Use the read-only assertions so CI fails loudly on drift or a stale lock — never
silently reconciles:

```
qvr sync --check       # read-only: exit non-zero if project is out of sync; writes nothing
qvr sync --locked      # restore worktrees but make NO lock changes; fail if lock is stale or drifted
qvr sync --frozen      # restore strictly from the lock as-is; never re-resolve or rewrite it
```

A typical CI step:

```
qvr sync --locked && qvr list
```

`--check` is the lightest gate (does not even touch the filesystem). `--locked`
additionally restores worktrees so the rest of the job can run against real
skills, while still failing if a sync *would* have to modify `qvr.lock`.

## Gotchas

- **Pin on the export side, not the import side.** `export --frozen` bakes a
  `--commit=<sha>` into each manifest line; a plain `qvr import` of that manifest
  reproduces exact bytes. A *ref-only* manifest (no `--frozen` export) re-resolves
  to the ref tip on import — so the pin you keep or lose is decided at export.
  Don't reach for `import --frozen` to "keep the pin": it's a re-import drift-guard
  that needs an existing `qvr.lock` and fails on a from-scratch machine.
- **`--allow-drift` defeats the purpose.** It downgrades subtreeHash drift to a
  warning; it's a rare local-debug escape hatch and must never appear in CI.
- **Edit/link skills don't travel.** They live in the project tree. Export them
  with `--include-local` only as documentation; the recipient must re-create them
  (see `fork-and-publish-skill`).
- **Manifest ≠ lockfile.** The manifest is for bootstrapping a new environment;
  the lockfile is the reproducibility guarantee. Keep both in sync by re-exporting
  after you change the set.

## Troubleshooting

- *CI passes locally but fails in the pipeline* — the lock is likely stale or an
  entry drifted. Reproduce with `qvr sync --locked`; inspect with
  `qvr lock verify` and `qvr status` (see `verify-skill-supply-chain`).
- *Import installs a different commit than expected* — you imported a ref-only
  manifest. Re-export with `--frozen` (which embeds `--commit=` pins) and import
  it plainly; the pins are honored without an import-side `--frozen` flag.
- *`--frozen requires a readable lock file`* — you passed `import --frozen` on a
  fresh machine with no `qvr.lock`. Drop `--frozen`: a plain `import` of a frozen
  (commit-pinned) manifest is already byte-exact.
- *A skill silently missing after import* — it was an `edit`/`link` (local) skill,
  skipped by default. Re-create it in the project.
