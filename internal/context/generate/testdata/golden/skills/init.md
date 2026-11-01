---
name: init
description: Orient in this repository before the first change. Use at the start of a task to learn its languages, commands and accepted facts.
---

<!-- cascade:managed:begin skill-init -->
# Init

Learn the repository before you change it.

1. Read `.claude/CLAUDE.md` and the files under `.claude/rules/`.
2. Confirm the detected languages: `go`
3. Confirm the commands work before you rely on them.
   - Build: `make build`
   - Test: `go test ./...`
   - Lint: `golangci-lint run`
4. Note the accepted facts: `` layout: commands live in `cmd/` ``
5. State what you learned and what is still unknown before you plan.
<!-- cascade:managed:end -->
