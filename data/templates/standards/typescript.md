---
id = "standard-typescript"
version = "1.0.0"
tier = "any"
stacks = ["typescript"]
project_shapes = []
description = "TypeScript coding standard: strict mode, no any, ESM, typed boundaries, co-located tests, lint-on-save."
---

# TypeScript Coding Standard

## Compiler

- `strict: true` in every `tsconfig.json`. No exceptions, no overrides.
- No `any` — use `unknown` and narrow, or define a proper type.
- `noUncheckedIndexedAccess: true` where the project targets Node 18+.
- Keep `tsconfig.json` at the package root; extend a shared base config when the
  repo contains multiple packages.

## Module Format

- Publish dual CJS + ESM via a bundler (e.g. tsup or rollup). Source is always
  `.ts`; no `.js` files in `src/`.
- Use named exports; avoid default exports in library code (they hurt tree-shaking
  and refactoring).
- `"type": "module"` in `package.json` for ESM-first packages; include explicit
  `.cjs` entry in `exports` for CommonJS consumers.

## Code Style

- Format with Prettier; lint with ESLint (`@typescript-eslint` recommended set).
- Run both on save in the editor and as a pre-commit check.
- Files: ≤300 lines. Functions: ≤50 lines. Split by domain when limits are reached.
- One concern per file: types, utils, hooks, services are separate modules.

## Typing Boundaries

- Every exported function and class has explicit parameter and return types.
- Avoid type assertions (`as Foo`) except at validated external boundaries (JSON
  parse, DOM events). Add a comment explaining why when you must.
- Prefer interfaces for object shapes that will be implemented or extended;
  prefer type aliases for unions and mapped types.

## Tests

- Co-locate tests: `foo.ts` → `foo.test.ts` in the same directory.
- Run with Vitest (or the project's chosen runner); coverage target ≥80%.
- Test the public API, not implementation details.
- No skipped tests committed to main.

## Documentation

- TSDoc (`/** … */`) on every exported symbol: one-line summary + `@param` +
  `@returns` + `@throws` where relevant.
- Keep examples in doc comments runnable — they serve as lightweight tests.
