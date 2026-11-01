---
name: verify
description: Prove a change works by running the repository's checks and reading their output. Use before calling any task done.
---

# Verify

Done means the named checks ran and passed.

1. Run the build: {{build}}
2. Run the tests: {{test}}
3. Run the linter: {{lint}}
4. Read each result. A skipped, filtered or unread check has not passed.
5. Report each command with its result. Report a failure as a failure.
