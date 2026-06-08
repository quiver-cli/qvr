# Skill Format Specification

qvr follows the **agentskills.io specification** for skill format. Full spec: https://agentskills.io/specification

## Directory Structure

A skill is a directory containing, at minimum, a `SKILL.md` file:

```
skill-name/
├── SKILL.md          # Required: metadata + instructions
├── scripts/          # Optional: executable code
├── references/       # Optional: documentation
├── assets/           # Optional: templates, resources
└── ...               # Any additional files
```

## SKILL.md Format

YAML frontmatter followed by Markdown content:

```yaml
---
name: pdf-processing
description: >
  Extract text and tables from PDF files, fill PDF forms,
  and merge multiple PDFs. Use when working with PDF documents
  or when the user mentions PDFs, forms, or document extraction.
license: Apache-2.0
compatibility: Requires Python 3.14+ and uv
metadata:
  author: example-org
  version: "1.0"
allowed-tools: Bash(git:*) Bash(jq:*) Read
---

# PDF Processing

## Instructions
...
```

## Frontmatter Fields

### `name` (Required)

- 1-64 characters
- Lowercase alphanumeric (`a-z`, `0-9`) and hyphens (`-`) only
- Must NOT start or end with a hyphen
- Must NOT contain consecutive hyphens (`--`)
- **Must match the parent directory name**

Valid: `pdf-processing`, `code-review`, `data-analysis`
Invalid: `PDF-Processing`, `-pdf`, `pdf--processing`, `pdf-`

### `description` (Required)

- 1-1024 characters
- Should describe both what the skill does AND when to use it
- Include specific keywords that help agents identify relevant tasks

Good: "Extracts text and tables from PDF files, fills PDF forms, and merges multiple PDFs. Use when working with PDF documents."
Poor: "Helps with PDFs."

### `license` (Optional)

License name or reference to a bundled license file. Keep short.

```yaml
license: MIT
license: Proprietary. LICENSE.txt has complete terms
```

### `compatibility` (Optional)

- 1-500 characters
- Environment requirements: intended product, system packages, network access

```yaml
compatibility: Designed for Claude Code (or similar products)
compatibility: Requires git, docker, jq, and access to the internet
```

### `metadata` (Optional)

Arbitrary key-value map (string → string). For additional properties not in the spec.

```yaml
metadata:
  author: example-org
  version: "1.0"
  homepage: https://github.com/example-org/skills
```

### `allowed-tools` (Optional, Experimental)

**Space-delimited** list of pre-approved tools. NOT an array.

```yaml
allowed-tools: Bash(git:*) Bash(jq:*) Read Grep Glob
```

## Progressive Disclosure

Skills are structured for efficient context usage:

| Tier | Content | When Loaded | Size |
|------|---------|-------------|------|
| Metadata | `name` + `description` | Startup (all skills) | ~100 tokens |
| Instructions | Full SKILL.md body | Skill activates | <5000 tokens recommended |
| Resources | scripts/, references/, assets/ | On demand | Varies |

**Keep SKILL.md under 500 lines.** Move detailed content to reference files.

## File References

Use relative paths from skill root. Keep one level deep:

```markdown
See [the reference guide](references/REFERENCE.md) for details.
Run: scripts/extract.py
```

## Optional Directories

### `scripts/`

Executable code agents can run. Should be:
- Self-contained or clearly document dependencies
- Include helpful error messages
- Handle edge cases
- Output JSON to stdout, status to stderr

### `references/`

Additional documentation agents read on demand:
- Keep files focused and small
- Examples: REFERENCE.md, FORMS.md, domain-specific files

### `assets/`

Static resources: templates, images, data files, schemas.

## Complex Skills (Multi-Rule Pattern)

For skills with many rules, compile them into AGENTS.md:

```
react-best-practices/
├── SKILL.md              # Entry point (<500 lines)
├── AGENTS.md             # Generated: compiled from all rules
├── metadata.json         # Additional metadata
├── rules/                # Individual rule files (can be 70+)
│   ├── _sections.md
│   ├── async-parallel-fetch.md
│   └── ...
├── references/
└── scripts/
    └── build.sh          # Compile rules → AGENTS.md
```

## Lint

```bash
# Official validator
skills-ref validate ./my-skill

# qvr lint (also checks custom rules)
qvr lint ./my-skill
qvr lint --output json
```
