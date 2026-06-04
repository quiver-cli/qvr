---
name: fork-and-publish-skill
description: >
  Customizes an installed agent skill and ships it back upstream or as a versioned
  fork using qvr's edit/publish authoring loop. Use when a user wants to modify,
  customize, fork, release, or publish a qvr skill — e.g. "edit this skill",
  "publish my changes", "fork a skill to my own repo", "cut a v1.0.0 release of a
  skill", or "iterate on a skill and tag new versions". Covers qvr edit, diff,
  status, and publish (--fork --migrate --tag, --auto-commit, root vs nested
  layout), including the consume-mode round trip.
metadata:
  author: quiver-playground
  version: "1.0.0"
---

# Fork and publish a skill with qvr

qvr lets you take an installed skill, **eject** it into your project to edit
directly, then **publish** the result — either back to its origin or to a brand
new fork you own — cutting tagged releases as you go. After each publish the lock
entry flips back to consume mode, so you can iterate: edit → publish v0.2.0 →
edit → publish v0.3.0. This skill drives that loop.

## When to use this

- The user wants to tweak a skill's content and keep the change.
- They want to publish a skill back upstream, or fork it to a repo they control.
- They want to cut tagged releases and iterate version by version.

For installing/discovering skills use `onboard-skills`; for verifying provenance
of what you publish use `verify-skill-supply-chain`.

## Prerequisites

- The skill must be **installed in a project** (not only `--global`). Editing a
  global skill in place would mutate a copy every project shares, so it's not
  supported. To change a global skill: `qvr add <skill>` into a project, edit and
  publish there, then re-add the published version with `--global`.
- A clean-ish working state helps; `publish` refuses a dirty eject dir unless you
  pass `--auto-commit`.

## Workflow

### 1. Eject the skill for editing

`qvr edit` promotes the symlinked skill into a real directory under the canonical
agent target (the alphabetical-first installed target — e.g. `.claude/skills/<name>/`
or `.cursor/rules/<name>/`, depending on what you installed into); any other
installed target dirs become relative symlinks to it. The lock entry gains
`mode = 'edit'`.

```
qvr edit my-skill
qvr edit my-skill --author "Jane Dev" --email jane@example.com   # initial-commit identity
```

It's idempotent — running it again after the first eject is a no-op.

### 2. Make and review changes

Edit the files in the ejected skill directory (the canonical agent target), then
review:

```
qvr diff my-skill           # git diff in the skill worktree
qvr diff my-skill --stat
qvr status my-skill         # dirty state + git ahead/behind
```

### 3a. Publish back to the original upstream

```
qvr publish my-skill -m "Fix edge case" --tag v1.0.1
qvr publish my-skill --dry-run -m "..."     # validate and stage without pushing
qvr publish my-skill --auto-commit -m "..." # stage+commit a dirty eject dir first
```

### 3b. Fork to a new repo you own

Use `--fork <git-url>` to retarget the push, and `--migrate` to rewrite the lock
entry so **future** publishes track the fork:

```
qvr publish my-skill --fork https://github.com/me/my-skill.git --migrate --tag v0.1.0
```

`--fork --migrate` auto-registers the fork as a config registry named
`<org>/<repo>` (e.g. `me/my-skill`). To re-add the skill from it later, use that
**full** name:

```
qvr add my-skill --registry me/my-skill
```

`--layout` controls the published repo shape: `root` (single-skill repo, the
default for `--fork`) or `nested` (multi-skill registry under `skills/<name>/`,
the default otherwise).

### 4. Iterate releases

Each publish flips the entry back to consume mode, so the loop repeats cleanly:

```
qvr edit my-skill
# ...changes...
qvr publish my-skill --tag v0.2.0
qvr edit my-skill
# ...changes...
qvr publish my-skill --tag v0.3.0
```

Consumers pick up a release with the version manager:

```
qvr outdated                 # which installs have newer upstream commits
qvr switch my-skill v0.3.0   # explicit pin
qvr upgrade my-skill         # jump to the latest semver tag (alias of switch --latest)
qvr pull my-skill            # fast-forward the current ref (alias of switch --tip)
```

### 5. Add a new skill to a registry (greenfield path mode)

To publish a brand-new local skill directory into a multi-skill registry repo:

```
qvr publish ./my-new-skill --registry me/agent-skills -m "Add my-new-skill"
```

## Gotchas

- **Dirty eject dirs are refused.** `publish` won't push uncommitted changes
  unless you pass `--auto-commit` (which stages + commits first).
- **`--migrate` only matters with `--fork`.** Without it, the fork push is
  one-off and the lock entry still tracks the original upstream.
- **Registry name after a fork is the full `<org>/<repo>`**, not a short alias —
  `qvr add … --registry`, `registry list`, and `registry update` all need it.
- **Clean up edit ejects before teardown.** A `sync` with a drifted/ejected entry
  **fails** asking for `--allow-drift`; publish or revert the eject first (or
  re-consume the skill) before a `cache clean` + `sync`.
- **Lockfile/HEAD mismatch** is refused for integrity; only override with
  `--allow-lockfile-heal` if you understand why HEAD diverged.

## Troubleshooting

- *"refusing dirty working directory"* — commit the eject, or pass `--auto-commit`.
- *Second release fails to resolve the commit* — make sure each iteration goes
  through `qvr edit` again; the entry must be back in edit mode to publish.
- *`qvr add --registry <alias>` can't find the fork* — use the full `<org>/<repo>`
  name that `--fork --migrate` registered, visible in `qvr registry list`.
- *Want to undo an eject without publishing* — re-consume the skill (re-`add` /
  `switch` to a ref); confirm with `qvr status` and `qvr sync`.
