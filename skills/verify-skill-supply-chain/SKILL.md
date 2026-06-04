---
name: verify-skill-supply-chain
description: >
  Vets and continuously verifies the integrity and provenance of agent skills
  installed with qvr. Use when a user cares about skill security, trust, signing,
  tampering, or supply-chain integrity — e.g. "scan this skill for problems",
  "is this skill safe", "verify the skill hasn't drifted", "who is allowed to
  author this registry's skills", "check the signature", or "gate CI on skill
  integrity". Covers qvr scan, lock verify (--fail-on, --repair), trust
  pin/verify, and provenance.
metadata:
  author: quiver-playground
  version: "1.0.0"
---

# Verify the skill supply chain with qvr

A skill is code-adjacent: it ships instructions and files an agent will act on.
qvr gives four independent checks — a static **scan** of content, a content
**hash drift** check against the lockfile, an author **trust** policy per
registry, and **provenance** (source, pinning, signature). This skill combines
them into a vetting pass you can run at install time and re-run in CI.

## When to use this

- Before trusting a newly added or third-party skill.
- To prove an installed skill hasn't been altered since it was pinned.
- To enforce *who* is allowed to author skills in a given registry.
- To gate CI on integrity (fail on drift or unverified entries).

## The four checks

### 1. Scan content (static analysis)

`qvr scan` reads every file as a string — it never executes anything — and reports
categories such as prompt-injection patterns, leaked credentials, hidden/bidi
unicode, and risky permissions. The exit code is controlled by `--fail-on`
(default `error`).

```
qvr scan my-skill                                   # installed name or a path
qvr scan ./skills/my-skill --fail-on critical       # only fail on the worst
qvr scan my-skill --severity warning                # show warnings and up
qvr scan my-skill --against origin/main             # only findings new vs a ref
qvr scan my-skill --format sarif > scan.sarif       # machine-readable for CI (scan has no -o; redirect)
```

Install-time scanning is on by default (`security.scan_on_install`); `qvr add
--no-scan` opts out for a single install (prefer not to).

### 2. Verify content integrity (hash drift)

`qvr lock verify` recomputes each entry's subtree hash from disk and compares it
to the recorded value, reporting `ok`, `drift`, `unverified` (no recorded hash),
`missing` (worktree gone), `link`, or `failed`. As of 0.10.x it exits non-zero on
drift by default so CI can gate:

```
qvr lock verify                          # default: drift/missing/failed are fatal
qvr lock verify --fail-on unverified     # also fail when an entry has no recorded hash
qvr lock verify --fail-on none           # report only (old behavior, exit 0)
```

Two repair/maintenance paths:

```
qvr lock verify --repair                 # rewrite Verification blocks from current disk
qvr lock upgrade                          # backfill missing Verification blocks
```

Only `--repair` when you **trust the current worktree** — it re-pins to whatever
is on disk, drift and all.

### 3. Enforce author trust policy

Pin which commit authors a registry is allowed to ship from, then verify
installed skills against that policy:

```
qvr trust list                                       # trusted authors by registry
qvr trust pin <org>/<repo> "Trusted Author"          # allow an author for one registry
qvr trust verify                                     # check installs vs policy
qvr trust verify --all                               # project + global
qvr trust unpin <org>/<repo>                          # remove the registry's pins
```

Use the full `<org>/<repo>` registry name.

### 4. Inspect provenance and signatures

```
qvr provenance my-skill        # source, subdir, pinned commit/tree, scan status, signature
qvr provenance my-skill --all  # search project + global
```

`provenance` is honest about limits: a `verified` signature means git's
`verify-tag` / `verify-commit` was satisfied — it does **not** by itself mean the
author is trusted (that's what `trust` is for). An unsigned skill installs unless
`security.require_signed` is enabled; an *invalid* signature is refused at install
time.

## A vetting pass (recommended order)

1. `qvr scan my-skill --fail-on error` — reject obviously hostile content.
2. `qvr provenance my-skill` — confirm source, pinning, and signature status.
3. `qvr trust verify` — confirm the author is allowed for that registry.
4. `qvr lock verify` — confirm nothing has drifted since it was pinned.

In CI, chain the gating forms:

```
qvr scan --fail-on error ./skills/my-skill && qvr lock verify --fail-on drift
```

## Gotchas

- **Scanner reports, never executes.** The executable bit is reported, not
  honoured. A clean scan is necessary, not sufficient — still read what a skill
  does.
- **`--repair` can launder drift.** It re-pins to current disk; use it only after
  you've confirmed the worktree is the intended content.
- **Signed ≠ trusted.** Pair `provenance` (signature valid?) with `trust verify`
  (author allowed?). They answer different questions.
- **`--no-scan` and `--allow-drift` are escape hatches**, not defaults — keep them
  out of CI.
- **`--fail-on none` hides problems.** It restores the old report-only behavior;
  fine for a dashboard, wrong for a gate.

## Troubleshooting

- *`lock verify` reports `unverified`* — the entry predates Verification blocks.
  Run `qvr lock upgrade` to backfill, then re-verify.
- *Drift you didn't cause* — the worktree was edited (maybe an `edit` eject) or
  the cache was touched. Inspect with `qvr diff <skill>` and `qvr status`; restore
  with `qvr sync` (without `--allow-drift`).
- *`trust verify` fails after a fork* — the fork registers a new `<org>/<repo>`;
  pin the intended author for that registry name.
- *Signature shows `none`* — upstream didn't sign the tag/commit. Decide via
  policy (`security.require_signed`) rather than ad hoc.
