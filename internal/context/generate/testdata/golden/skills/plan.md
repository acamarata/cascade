---
name: plan
description: Turn a task into a short, checkable plan before writing code. Use when a task needs more than one change or names more than one file.
---

<!-- cascade:managed:begin skill-plan -->
# Plan

Write the plan before the code.

1. Restate the task and its acceptance in your own words.
2. List the files you expect to change and why each one.
3. Name the test that proves each acceptance item.
4. Name the commands that will verify the work.
   - Build: `make build`
   - Test: `go test ./...`
   - Lint: `golangci-lint run`
5. State every assumption. Ask when an assumption changes the result.
<!-- cascade:managed:end -->
