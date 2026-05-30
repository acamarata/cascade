# Personal Cascade Instructions (PCI)

**Tier:** PCI — applies to ALL coding projects under `<PROJECTS_ROOT>/`.
**Subordinate to:** GCI. PCI inherits GCI rules; this file adds coding-specific cross-project doctrine.
**Sibling files:** `AGENTS.md` and `CLAUDE.md` are symlinks to this file.

---

## Cascade Position

```
GCI  (~/.cascade/CASCADE.md)                All AI work
  └─ PCI  (<PROJECTS_ROOT>/.cascade/)   ← THIS FILE
      ├─ <PRODUCT_A>/.cascade/           APC tier
      │   ├─ <REPO_1>/.cascade/          PRC tier
      │   └─ <REPO_2>/.cascade/          PRC tier
      └─ <PRODUCT_B>/.cascade/           APC tier
```

Higher tiers always win. PCI adds coding doctrine; it never weakens GCI rules.

---

## Backend Policy (Hard Rule)

Every project that needs a backend uses a single, consistent backend adapter. No per-project hand-rolled servers, no direct database connections, no duplicated service layers.

**The singleton principle:** one backend adapter per product, shared across all apps. All data access goes through the adapter's abstraction layer, never directly to storage.

**CLI gaps:** when the backend adapter CLI cannot do what is needed, file an inbox message to the adapter project's inbox. Never work around the CLI with direct database writes or hand-edited configuration on live.

**Full rule:** `.cascade/rules/backend-adapter-singleton.md`

---

## Frontend Stack Selection

Use the framework that matches the use case. Deviate only with a Stack Deviation ADR in the PRC.

| Use case | Framework |
|---|---|
| Marketing sites, SEO-heavy | Framework with SSR/SSG support |
| Web apps, dashboards | SPA framework (Vite/bundler-native) |
| Desktop apps | Native wrapper (e.g., Tauri) + SPA |
| Mobile apps | Cross-platform mobile framework |
| CLI tools | Language-native; no UI framework |

**TypeScript strict mode** for all JS/TS projects. `pnpm` only as package manager.

**Full rule:** `.cascade/rules/frontend-stack-selection.md`

---

## Modular Coding (Hard Rule)

All code is broken into the smallest reusable units. Every unit has known inputs, known outputs, and is independently testable. Duplication is a bug.

**File size limit:** 500 lines maximum. Split at that threshold.

**Shared code strategy:**

| Scope | Mechanism |
|---|---|
| Same app | `utils/`, `lib/`, `hooks/`, `components/` |
| Same repo (cross-app) | `packages/<name>/` in workspace |
| Cross-repo (same product) | Published under product scope |
| Cross-product (general utility) | Published as standalone package |

**Publishing threshold:** if a utility is used across 2+ products, publish it as a standalone package. If it is backend logic used in 2+ products, expose it through the backend adapter plugin system.

**Full rule:** `.cascade/rules/modular-coding.md`

---

## Engineering Excellence (Hard Rule)

Every codebase under `<PROJECTS_ROOT>/` reflects engineering excellence: framework-native structure, DRY composition, typed boundaries, tests, and documented signatures.

**Universal requirements:**

- TypeScript strict mode (`strict: true`)
- Lint on save (configured per project)
- Test coverage: unit for utils/hooks, integration for API routes, E2E for critical flows
- CI passes before deploy (`.github/workflows/` required)

**Documentation cadence:** at the end of every ticket, before marking done:

1. Update the relevant master list entry
2. Update `.cascade/memory/decisions.md` if a non-obvious choice was made
3. Update `.cascade/memory/lessons.md` if a gotcha was discovered
4. Update `.github/wiki/` or `.github/docs/` for user-visible changes
5. Add a changelog entry for user-visible changes

A task is not done until docs are updated.

**Full rule:** `.cascade/rules/excellence-in-engineering.md`

---

## SPORT — Single Point of Reference Truth

`.cascade/phases/sport/` contains master lists of every tracked entity: packages, components, hooks, endpoints, routes, DB tables, CLI commands, env vars. Each entity has a status (done/partial/planned/in-progress/blocked).

**Read before claiming any task.** Update after every task completes.

**Subagent dispatch rule:** every spawned agent prompt must include the relevant SPORT paths so the agent reads them first.

**Full rule:** `.cascade/rules/sport-first-protocol.md`

---

## Phase State Paths (PCI Level)

All active phase state under `<PROJECTS_ROOT>/` lives at:

```
<PRODUCT_ROOT>/.cascade/phases/current/p<N>/
  phase.yaml         — phase header
  status.yaml        — counters (also mirrored at top-level phases/)
  plan.md            — human-readable plan
  epics/             — PEWS YAML tree
```

Both harnesses read and write the same `.cascade/phases/` directory. There is one canonical state — not one per harness.

---

## Inbox Protocol

`.cascade/inbox/` enables cross-project messaging. Use `cascade-send` to write inbox messages; never use the Write tool directly on inbox paths.

Check `.cascade/inbox/` at every session start. Archive after processing.

**Full protocol:** `.cascade/rules/inbox-protocol.md`

---

## Project Scope Isolation (Hard Rule)

The active working directory defines the scope. Never read, modify, or draw context from files outside the current project unless the user explicitly requests it.

- Workspace = scope. Only files under the active working directory are in scope.
- No cross-project memory bleed. Patterns and decisions from Project A do not carry to Project B.
- No passive cross-project inference. Apply only what is documented in the current project's cascade files.
- Explicit exception only: cross-project reference requires explicit user instruction.

**Full rule:** `.cascade/rules/project-scope-isolation.md`
