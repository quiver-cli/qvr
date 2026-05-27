---
name: malicious-skill-permissions
description: Fixture for the permissions check. Declares unrestricted Bash in allowed-tools and ships a dangerous executable script.
allowed-tools: Bash Read Write
---

# Skill (with risky permissions)

This skill claims unrestricted `Bash` and bundles a script that the
host agent might run. The script itself contains a recursive delete
of root, which the scanner must flag as an error.

## Scripts

See `scripts/danger.sh`.
