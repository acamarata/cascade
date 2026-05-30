# Per-Project Cascade (PPC)

**Tier:** PPC — applies to ONE project (single repo or workspace).
**Subordinate to:** GCI → PCI → APC. PPC adds project-specific tech stack, conventions, and master list locations.
**Sibling files:** `AGENTS.md` and `CLAUDE.md` are symlinks to this file.

---

## Cascade Position

```
GCI  (~/.cascade/CASCADE.md)
  └─ PCI  (<PROJECTS_ROOT>/.cascade/)
      └─ APC  (<PRODUCT_ROOT>/.cascade/)
          └─ PPC  (<PROJECT_ROOT>/.cascade/)   ← THIS FILE
              └─ <APP>/.cascade/                PAC tier (if multi-app)
```

---

## Project Identity

**Project name:** `<PROJECT_NAME>`
**Repository:** `<GITHUB_ORG>/<REPO_SLUG>`
**Language:** `<PRIMARY_LANGUAGE>`
**Framework:** `<PRIMARY_FRAMEWORK>`
**Package manager:** pnpm (JS/TS) or language-native equivalent

---

## Tech Stack

| Layer | Choice | Notes |
|---|---|---|
| Language | `<LANGUAGE>` | |
| Framework | `<FRAMEWORK>` | |
| Testing | `<TEST_FRAMEWORK>` | |
| Linting | `<LINTER>` | Config at `<CONFIG_PATH>` |
| CI | GitHub Actions | `.github/workflows/ci.yml` |

**Stack Deviation ADRs:** any deviation from the PCI stack matrix is documented in `.cascade/phases/current/adrs/`.

---

## Source Layout

```
<PROJECT_ROOT>/
├── src/                 — application source
│   ├── components/      — reusable UI units (if applicable)
│   ├── hooks/           — reusable stateful logic (if applicable)
│   ├── lib/             — domain business logic
│   ├── utils/           — pure functions, no side effects
│   ├── services/        — external integrations
│   ├── types/           — shared type definitions
│   └── ...
├── tests/               — test files (or co-located per framework convention)
├── .cascade/            — AI working memory (gitignored)
└── .github/
    ├── workflows/       — CI/CD
    └── wiki/            — human docs (public) or docs/ (private)
```

Adjust to match the framework's idiomatic layout. Document any structural deviation here.

---

## Environment Variables

All env vars are listed in `.cascade/docs/MASTER-ENV.md`. Required vars are documented in `.env.example` at the project root.

| Variable | Purpose | Required |
|---|---|---|
| `<VAR_NAME>` | `<purpose>` | Yes |

---

## Master Lists

| List | File | Tracks |
|---|---|---|
| Features | `.cascade/docs/MASTER-FEATURES.md` | Every feature with status |
| Components | `.cascade/docs/MASTER-COMPONENTS.md` | Reusable UI components |
| Routes | `.cascade/docs/MASTER-ROUTES.md` | API + frontend routes |
| Env vars | `.cascade/docs/MASTER-ENV.md` | Required env vars |

Check before implementing. Update immediately after implementing.

---

## Task Pipeline

```
ideas/ → planning/ → tasks/queue.md → tasks/active.md → tasks/done.md
```

`active.md` holds ONE phase at a time (max ~200 lines). On phase complete: move summary to `done.md`, clear `active.md`, load next from queue.

---

## QA Gates

Run before every commit: `.cascade/qa/pre-commit.md`
Run before every PR: `.cascade/qa/pre-pr.md`

---

## Memory

| File | Update when |
|---|---|
| `.cascade/memory/decisions.md` | Any significant technical choice |
| `.cascade/memory/lessons.md` | A gotcha or mistake is discovered |
| `.cascade/memory/patterns.md` | A codebase convention is established |
