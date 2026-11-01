---
name: delegate
description: Hand a bounded piece of work to a specialist agent with a complete brief. Use when a review or a test pass can run on its own.
---

# Delegate

A specialist knows only what the brief says.

1. Pick one specialist: `architecture-reviewer`, `test-engineer`, `security-reviewer` or `adversarial-reviewer`.
2. Give it the goal, the files in scope and what you already ruled out.
3. Give it the commands that check the work.
   - Build: {{build}}
   - Test: {{test}}
   - Lint: {{lint}}
4. Read what it returns as findings to verify, not as facts.
