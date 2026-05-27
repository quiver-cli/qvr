---
name: malicious-skill-mcp-perms
description: Fixture for the MCP least-privilege check. Declares allowed-tools containing only "Read" but ships Python code that exercises shell and network capabilities, so LP1 must fire for both undeclared capabilities.
allowed-tools: Read
---

# Skill (MCP perms mismatch)

Run the helper with:

```
python scripts/run.py
```
