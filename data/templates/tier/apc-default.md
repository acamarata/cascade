---
id = "apc-default"
version = "1.0.0"
tier = "apc"
stacks = []
project_shapes = []
description = "Vendor-neutral default instructions for the All-Projects Cascade tier."
---

# All-Projects Cascade Instructions (APC)

This file applies to every project inside the development tree at
`{{DEV_TREE_PATH}}`. It holds shared stack constraints, cross-project
conventions, and rules that apply to all projects equally.

Place project-specific rules in the project's PPC file, not here.

## Dev Tree

Root path: `{{DEV_TREE_PATH}}`
Org / account: {{ORG_OR_ACCOUNT}}

Projects in this tree:

| Project | Path | Description |
|---|---|---|
| {{PROJECT_1_NAME}} | `{{PROJECT_1_PATH}}` | {{PROJECT_1_DESC}} |
| {{PROJECT_2_NAME}} | `{{PROJECT_2_PATH}}` | {{PROJECT_2_DESC}} |

Add a row for each project. Keep this list accurate — it is the source of
truth for cross-project scope isolation.

## Shared Stack Constraints

These constraints apply to every project unless a PPC file overrides them
with an explicit documented exception.

- Package manager: {{PACKAGE_MANAGER}} (e.g. pnpm, cargo, pip)
- Language: {{LANGUAGE}} strict mode where applicable
- Formatter: {{FORMATTER}} — run before every commit
- Linter: {{LINTER}} — zero warnings policy
- Test framework: {{TEST_FRAMEWORK}}

Do not mix package managers within the dev tree. If a project needs a
different tool, document the reason in that project's PPC file.

## Cross-Project Rules

- Do not apply knowledge, credentials, or context from one project to another
  without explicit instruction.
- Shared libraries live in {{SHARED_LIB_PATH}} and are the canonical source.
  Do not copy shared code into individual projects.
- When a change touches multiple projects, list all affected projects in the
  task description before starting work.
- Infrastructure (CI/CD, DNS, hosting) is documented per-project in the PPC
  file. Do not guess infra details from context.

## Security Baseline

- No credentials, tokens, or API keys in version-controlled files.
- Secrets belong in {{SECRET_STORE}} (e.g. an env file, a keychain, a vault).
- Before publishing or deploying anything, confirm the recipient / target is
  correct and no sensitive data is included.

## Documentation Standard

Every shipped feature must have updated docs before the task is marked done.
Docs location: {{DOCS_PATH}} (e.g. `.github/wiki/` or `.github/docs/`)
