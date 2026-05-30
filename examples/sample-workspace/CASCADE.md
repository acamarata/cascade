# PRC — sample-workspace Repo Rules

**Tier:** PRC (Per-Repo Cascade)
**Repo:** sample-workspace
**Inherits from:** GCI → APC → PPC (project level)
**This file is the canonical source.** AI tool files (CLAUDE.md, AGENTS.md, .cursorrules, etc.)
are derived from this file by Cascade.

---

## Repo Identity

- **Type:** TypeScript library
- **Package manager:** pnpm
- **Build:** tsup (dual CJS+ESM output)
- **Test runner:** Node built-in test runner (`node --test`)
- **Lint:** ESLint + Prettier
- **CI:** GitHub Actions (Node matrix 20/22/24)

---

## Layout

```
src/
  index.ts          # Public API surface — export from here only
  lib/              # Domain logic
  utils/            # Pure functions, no side effects
  types/            # Shared type definitions
dist/               # Build output (gitignored)
test/               # Tests mirror src/ structure
```

Files match the TypeScript library conventions defined at the APC tier. Nothing here
deviates from the standard layout.

---

## Public API Contract

The `src/index.ts` re-exports are the public API. Any other path is internal.

Breaking the public API requires a major version bump. Additions are minor.
Bug fixes are patch. This repo follows semantic versioning strictly.

---

## Testing Convention

Tests live in `test/` and mirror `src/` structure: `src/lib/foo.ts` has
`test/lib/foo.test.ts`. Test files use `.test.ts` suffix, not `.spec.ts`.

Every exported function has at minimum:
- One happy-path test
- One edge-case test covering the documented boundary conditions
- One failure test for invalid input (throws or returns expected error)

---

## Dependencies

This repo has zero runtime dependencies. Dev dependencies only.

Before adding any runtime dependency, check: can this be done without it?
If yes, implement without it. If no, document why in `.cascade/memory/decisions.md`
and open a PR with the rationale.

---

## Memory

This repo maintains three memory files in `.cascade/memory/`:

| File | Contains |
|---|---|
| `decisions.md` | Why non-obvious technical choices were made |
| `lessons.md` | Gotchas discovered, mistakes made, fixed approaches |
| `patterns.md` | Coding conventions specific to this codebase |

AI agents read these at session start. Update them as you work.
