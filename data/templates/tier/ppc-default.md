---
id = "ppc-default"
version = "1.0.0"
tier = "ppc"
stacks = []
project_shapes = []
description = "Vendor-neutral default instructions for the Per-Project Cascade tier."
---

# Per-Project Cascade Instructions (PPC)

This file applies to the project at `{{PROJECT_ROOT}}`. It inherits from GCI,
PCI (if applicable), and APC. Rules here are specific to this project and do
not affect other projects in the dev tree.

## Project Overview

Name: {{PROJECT_NAME}}
Path: `{{PROJECT_ROOT}}`
Purpose: {{PROJECT_PURPOSE}}
Status: {{PROJECT_STATUS}} (e.g. active, maintenance, archived)

## Repo Inventory

| Repo | Path | Purpose |
|---|---|---|
| {{REPO_1_NAME}} | `{{REPO_1_PATH}}` | {{REPO_1_PURPOSE}} |
| {{REPO_2_NAME}} | `{{REPO_2_PATH}}` | {{REPO_2_PURPOSE}} |

Each repo has its own PRC file. Read the relevant PRC file before working in
a repo. Rules in a PRC file are specific to that repo and do not override this
PPC file.

## Shared Backend and Infrastructure

Backend: {{BACKEND_DESCRIPTION}}
Database: {{DATABASE_DESCRIPTION}}
Hosting: {{HOSTING_DESCRIPTION}}
CI/CD: {{CI_CD_DESCRIPTION}}
Domain(s): {{DOMAINS}}

## Cross-Repo Dependencies

List dependencies between repos that span this project:

- {{REPO_A}} depends on {{REPO_B}} via: {{DEPENDENCY_TYPE}}

Keep this list current. Cross-repo changes must list all affected repos
before starting work.

## Inbox (PPCi)

Each project has a per-project inbox directory at `.cascade/inbox/` inside
the project root. This inbox is called PPCi (Per-Project Cascade Inbox) and
is distinct from PCI (Personal Cascade Instructions, a tier).

PPCi is the cross-agent messaging channel for this project:
- Location: `{{PROJECT_ROOT}}/.cascade/inbox/`
- Naming: `{priority}-{slug}.md` (e.g. `01-add-auth-endpoint.md`)
- Use it to: queue work items for other agents, leave handoff notes, record
  blockers that a different agent in this project must resolve.

Do not put personal or sensitive information in the inbox — it may be read
by any agent working on this project.

## Mode

```
mode: {{MODE}}  # normal | phased
```

In `normal` mode, tasks are worked directly without a formal phase structure.
In `phased` mode, the phase state lives in `.cascade/phases/current/` and
tasks follow the Plan -> Build protocol documented in that directory.

## Key Decisions

Record significant technical decisions here or reference ADR files:

| Decision | Record | Date |
|---|---|---|
| {{DECISION_SUMMARY}} | `{{ADR_FILE}}` | {{DECISION_DATE}} |
