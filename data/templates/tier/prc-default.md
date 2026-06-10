---
id = "prc-default"
version = "1.0.0"
tier = "prc"
stacks = []
project_shapes = []
description = "Vendor-neutral default instructions for the Per-Repo Cascade tier."
---

# Per-Repo Cascade Instructions (PRC)

This file applies to the repository at `{{REPO_ROOT}}`. It inherits from GCI,
APC, and PPC. Rules here are specific to this repo.

## Repo Purpose

Name: {{REPO_NAME}}
Path: `{{REPO_ROOT}}`
Purpose: {{REPO_PURPOSE}}
Primary language: {{PRIMARY_LANGUAGE}}
Visibility: {{VISIBILITY}} (public / private)

## Tech Stack

| Layer | Technology | Version |
|---|---|---|
| Runtime | {{RUNTIME}} | {{RUNTIME_VERSION}} |
| Framework | {{FRAMEWORK}} | {{FRAMEWORK_VERSION}} |
| Build tool | {{BUILD_TOOL}} | {{BUILD_TOOL_VERSION}} |
| Test runner | {{TEST_RUNNER}} | {{TEST_RUNNER_VERSION}} |
| Formatter | {{FORMATTER}} | — |
| Linter | {{LINTER}} | — |

## File Layout

```
{{REPO_ROOT}}/
  src/           # Source files
  tests/         # Integration and end-to-end tests
  docs/          # Developer docs (or .github/wiki/)
  {{EXTRA_DIR}}  # {{EXTRA_DIR_PURPOSE}}
```

Adjust to match the actual layout of this repo. Delete rows that do not apply.

## Testing Convention

- Unit tests: co-located with source files, suffix `_test` or in a `tests/`
  submodule depending on language convention.
- Integration tests: in `tests/` at the repo root.
- Required before merge: all existing tests pass; new code has tests covering
  the main path and at least one error path.
- Coverage target: {{COVERAGE_TARGET}} (e.g. ≥80% statement coverage)

Run tests with: `{{TEST_COMMAND}}`

## Lint and Format

Run before every commit:

```
{{FORMAT_COMMAND}}
{{LINT_COMMAND}}
```

Zero warnings policy applies. Do not suppress warnings without a comment
explaining why.

## Commit Convention

Format: `{type}({scope}): {summary}`

Types: `feat` | `fix` | `refactor` | `test` | `docs` | `chore` | `ci`

Example: `feat(auth): add token refresh endpoint`

Keep the summary under 72 characters. Reference the task ID in the body when
working from a formal task queue.

## Branch Convention

Main branch: `{{MAIN_BRANCH}}` (e.g. `main`)
Feature branches: `feat/{slug}` or `fix/{slug}`
Protected branches: {{PROTECTED_BRANCHES}}

Do not force-push to protected branches.

## Key Files

| File | Purpose |
|---|---|
| `{{KEY_FILE_1}}` | {{KEY_FILE_1_PURPOSE}} |
| `{{KEY_FILE_2}}` | {{KEY_FILE_2_PURPOSE}} |
