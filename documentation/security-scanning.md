# Security Scanning

A skill is code-adjacent: it ships instructions and files an agent will act on.
`qvr scan` runs a static analysis over a skill's bytes — it **reads every file as
a string and never executes anything** — and the same scanner gates every
`qvr add` so unvetted content can't slip into the lock.

## What it checks

| Check | Catches |
| ----- | ------- |
| **Prompt injection** | Instructions that try to override the agent, exfiltrate context, or hijack tool use |
| **Leaked credentials** | API keys, tokens, private keys, and other secrets committed into skill files |
| **Hidden unicode** | Bidi overrides, zero-width characters, and homoglyphs used to hide instructions |
| **Risky permissions** | Over-broad `allowed-tools`, world-writable/executable files, and similar footguns |

Findings carry a severity: `info`, `warning`, `error`, or `critical`.

## `qvr scan`

```bash
qvr scan my-skill                       # an installed skill name, or a path
qvr scan ./skills/my-skill              # scan a directory directly
```

Flags:

| Flag | Default | Effect |
| ---- | ------- | ------ |
| `--severity info\|warning\|error\|critical` | `info` | Display floor — only show findings at or above this level |
| `--fail-on error\|warning\|info\|critical` | `error` | Exit non-zero when any finding meets or exceeds this severity |
| `--format text\|json\|sarif\|markdown` | `text` | Report format (takes precedence over `--output`) |
| `--against <ref>` | — | Only report findings **new** relative to a Git ref (baseline diff) |
| `--max-file-bytes <n>` | — | Per-file content cap for very large files |
| `--global` | — | Resolve the installed skill from the user-global lock |

```bash
qvr scan my-skill --fail-on critical          # only the worst findings fail the gate
qvr scan my-skill --severity warning          # show warnings and up
qvr scan my-skill --against origin/main       # only findings introduced vs. main
qvr scan ./skills/my-skill --format sarif > scan.sarif   # machine-readable for CI
```

`qvr scan` has no `-o` flag — redirect stdout (`> scan.sarif`) to write a report.

## The install-time gate

Every `qvr add` scans the resolved skill before it's recorded. A blocked install
is rolled back atomically — the skill never lands in `qvr.lock`. Behavior is
governed by config (`~/.quiver/config.yaml`):

| Key | Default | Effect |
| --- | ------- | ------ |
| `security.scan_on_install` | `true` | Scan on every install |
| `security.block_severity` | `critical` | Block the install at this severity or above |
| `security.require_scan` | `false` | Forbid the `--no-scan` bypass when `true` |
| `security.require_signed` | `false` | Require a verified Git signature to install |

```bash
qvr add code-review                 # scans, then pins the SHA + verdict
qvr add code-review --no-scan       # skip the scan for one install (refused if require_scan)
qvr config set security.require_scan true   # forbid --no-scan org-wide
```

The scan outcome is recorded on the lock entry's `verification` block — the
report hash, scanner version, and decision (`allowed` / `blocked` / `skipped`) —
so the lock itself attests that each installed skill was vetted. Surface it with
`qvr provenance <skill>`.

## In CI

Scan authored skills on pull requests, and gate consuming repos on their lock:

```yaml
# Authoring a registry — scan changed skills
- run: |
    curl -fsSL https://github.com/astra-sh/qvr/raw/main/install.sh | sh
    qvr scan skills/ --format sarif > scan.sarif   # upload as a SARIF report

# Consuming repo — prove nothing drifted from the vetted set
- run: |
    qvr lock verify --strict      # bytes match the lock
    qvr trust verify              # commit authors match registry policy
```

`--format sarif` plugs straight into GitHub code scanning. Pair scanning with the
integrity, trust, and provenance checks — see the
[verify-skill-supply-chain](../skills/verify-skill-supply-chain/SKILL.md) skill
for the full vetting pass, and [config-reference.md](config-reference.md) for the
`security.*` and `trust` keys.

## Limits

A scanner reports; it does not execute and it cannot prove intent. The executable
bit is reported, not honored. A clean scan is **necessary, not sufficient** —
still read what a skill does before trusting it.
