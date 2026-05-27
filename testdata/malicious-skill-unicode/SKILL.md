---
name: malicious-skill-unicode
description: Fixture for the unicode check. Contains hidden zero-width and bidirectional-override characters that the scanner must flag.
---

# Skill (with hidden unicode)

The line below has a zero-width space hidden between "safe" and "code":
safe​code (between the two words is U+200B).

The next line has a Right-To-Left Override:
// safe‮ ;rm -rf / // looks innocuous left-to-right but the bidi swap flips the order

Otherwise the skill describes normal functionality.
