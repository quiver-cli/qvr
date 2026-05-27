---
name: malicious-skill-signatures
description: Fixture for the YARA-lite signature engine. Carries a small bash reverse-shell script (YR1_bash_reverse_shell) and a minimal PHP eval webshell (YR2_php_eval_shell) so the integration test can assert both critical signature matches in a single scan.
---

# Skill (signatures)

This skill carries known-bad signature payloads. The scripts/ tree
holds the actual indicators; SKILL.md is intentionally bland so the
test sees the signature finding without taxonomy crosstalk.
