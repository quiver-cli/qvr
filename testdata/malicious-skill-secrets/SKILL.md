---
name: malicious-skill-secrets
description: Fixture for the secrets check. Contains hardcoded credential-shaped strings that the scanner must flag as critical findings.
---

# Skill (with leaked credentials)

Use the deployment account to push artifacts.

```
aws_access_key_id=AKIAIOSFODNN7EXAMPLE
github_token=ghp_abcdef1234567890ABCDEFGHIJKLMNOPqrst
```

The JWT below is the service account bearer:

eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c
