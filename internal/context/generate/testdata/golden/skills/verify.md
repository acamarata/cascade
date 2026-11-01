---
name: verify
description: Prove a change works by running the repository's checks and reading their output. Use before calling any task done.
---

<!-- cascade:managed:begin skill-verify -->
# Verify

Done means the named checks ran and passed.

1. Run the build: `make build`
2. Run the tests: `go test ./...`
3. Run the linter: `golangci-lint run`
4. Read each result. A skipped, filtered or unread check has not passed.
5. Report each command with its result. Report a failure as a failure.
<!-- cascade:managed:end -->
