# Creating a Skill

> **Status: stable.** Scaffolding, lint, and `qvr publish` all
> ship today.

## Scaffold

```bash
# Simple skill (just SKILL.md)
qvr init my-skill

# Medium skill (with references + scripts)
qvr init my-skill --type medium

# Complex skill (with rules directory)
qvr init my-skill --type complex
```

## Write Your SKILL.md

Every skill needs a SKILL.md file following the [agentskills.io specification](https://agentskills.io/specification).

### Frontmatter

```yaml
---
name: my-skill
description: >
  What this skill does and when to use it.
  Include keywords that help agents match tasks to this skill.
license: MIT
metadata:
  author: your-name
  version: "1.0"
---
```

**Critical**: `name` must match the directory name. Lowercase + hyphens only.

### Body

Write clear instructions for the agent:

```markdown
# My Skill

## Instructions

1. First, do X...
2. Then check Y...
3. Finally, output Z...

## Examples

### Input
User asks: "review this PR"

### Expected Behavior
1. Read the diff
2. Check for common issues
3. Provide feedback

## Edge Cases
- If no diff is available, ask the user to provide one
- If the diff is too large, focus on the most changed files
```

### Tips for Good Skills

- **Be specific** in the description — include trigger keywords
- **Keep SKILL.md under 500 lines** — move details to references/
- **Include examples** of inputs and expected behavior
- **Handle edge cases** explicitly
- **Scripts should output JSON to stdout**, status to stderr

## Add Supporting Files

### scripts/

Executable code agents can run:

```bash
# scripts/analyze.sh
#!/bin/bash
set -e

echo "Analyzing..." >&2
# ... do work ...
echo '{"result": "success", "findings": []}' # JSON to stdout
```

### references/

Detailed documentation agents read on demand:

```markdown
<!-- references/PATTERNS.md -->
# Common Patterns

## Pattern 1: ...
## Pattern 2: ...
```

### assets/

Templates, data files, schemas.

## Lint

```bash
qvr lint ./my-skill
qvr lint ./my-skill --output json
```

Checks: name format, description length, directory name match, frontmatter validity, file reference integrity.

## Security Scan

```bash
qvr scan ./my-skill
```

Checks for: prompt injection, leaked secrets, hidden unicode, risky permissions.

## Publish

```bash
# Publish to your default registry
cd my-skill
qvr publish

# Publish to a specific registry
qvr publish --registry team
```

## Development Workflow

Use `link` for local development:

```bash
# Symlink your local skill directly
qvr link ./my-skill --target claude

# Edit SKILL.md — agent sees changes instantly (symlink)
# No need to reinstall

# When ready, publish to registry
qvr publish --registry team
```

## Complex Skills (Rule-Based)

For skills with many rules (like a large multi-rule skill):

```bash
qvr init my-complex-skill --type complex
```

This creates:
```
my-complex-skill/
├── SKILL.md              # Entry point (<500 lines)
├── rules/
│   ├── _sections.md      # Section definitions
│   ├── _template.md      # Template for new rules
│   └── example-rule.md   # First rule
├── references/
├── scripts/
│   └── build.sh          # Compile rules → AGENTS.md
└── metadata.json
```

Write individual rules in `rules/`, then compile:
```bash
./scripts/build.sh        # Generates AGENTS.md from all rules
```
