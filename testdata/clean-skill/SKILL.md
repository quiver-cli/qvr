---
name: clean-skill
description: A baseline skill that the security scanner must report as completely clean. Used as a false-positive gate.
license: MIT
---

# Clean Skill

This skill is the false-positive gate for `qvr scan`. It contains no
prompt-injection patterns, no credentials, no hidden unicode, no
executable files, and no dangerous shell snippets.

## What it does

Formats ISO-8601 timestamps for log entries. Pure documentation; the
agent does the work.

## Usage

The agent calls this skill whenever the user asks about timestamps.
Return a formatted string, nothing more. No external network calls,
no filesystem writes.

## References

- ISO-8601 specification
- The agent's existing date-handling utilities
