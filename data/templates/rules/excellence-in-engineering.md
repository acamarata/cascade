---
id = "rule-excellence-in-engineering"
version = "1.0.0"
tier = "any"
stacks = []
project_shapes = []
description = "Framework-native layout, DRY, typed boundaries, tests, size caps, and comment blocks on every reusable unit."
---

# Excellence in Engineering

These rules apply to every code change regardless of language or framework.

## Layout

- Use the framework's native directory layout. Do not invent custom structures
  when a conventional one exists.
- Group by domain, not by type (prefer `user/` over `models/ + views/ + controllers/`
  at the top level where the framework allows it).

## DRY — Don't Repeat Yourself

- Extract shared logic on the third copy, not before.
- Reuse existing utilities before writing new ones.
- Duplication across modules is a bug, not a style choice.

## Size Caps

- **Files**: ≤300 lines. Split by domain when a file exceeds this.
- **Functions / methods**: ≤50 lines. Extract helpers for anything longer.
- These caps are hard limits, not targets. Shorter is better.

## Typed Boundaries

- Use the language's strictest type-checking mode (`strict` in TypeScript,
  `mypy --strict` in Python, `clippy -D warnings` in Rust, etc.).
- No `any`, `dynamic`, or equivalent escape hatches without a documented reason.
- Public API surfaces must be fully typed.

## Tests

- Co-locate unit tests with the code they test (e.g. `foo_test.go` next to `foo.go`,
  `__tests__/Foo.test.ts` next to `Foo.ts`).
- Every new function needs at least one test covering the happy path and one
  covering an error path.
- Integration tests live in `tests/` at the repo root.
- CI must be green before any merge.

## Comment Blocks on Reusable Units

Every exported function, class, or module must have a structured comment block:

```
Purpose:     What this unit does and why it exists.
Inputs:      Parameters, types, valid ranges, side-effect preconditions.
Outputs:     Return value, type, and any mutations.
Constraints: Performance characteristics, thread-safety, platform limits.
```

Comments explain *why*. The code shows *what*. Never restate the code in prose.

## Zero-Warning Policy

- No lint warnings, no compiler warnings, no suppressed errors.
- When suppressing a warning is unavoidable, add a comment explaining why.
