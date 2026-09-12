# Technical Decisions

One entry per decision. Newest at the top.

---

## 2026-05-15: Zero runtime dependencies

**Decision:** Ship with zero runtime dependencies.

**Why:** This is a utility library. Every runtime dependency becomes a transitive
dependency for every downstream user. Keeping the dep tree empty reduces install size,
removes vulnerability surface, and eliminates version conflict risk.

**Alternatives considered:**
- Using lodash for utility functions: rejected — the functions needed are small enough
  to implement inline, and adding lodash adds ~70KB to user bundle.
- Using a date library for timestamp handling: rejected — `Date` is sufficient for
  the ISO 8601 strings this library works with.

**Revisit trigger:** If we need a non-trivial algorithm that would take >200 lines to
implement correctly (e.g., a cryptographic primitive), reconsider.

---

## 2026-04-28: tsup for build tooling

**Decision:** Use tsup to produce dual CJS+ESM output.

**Why:** tsup handles the CJS/ESM split without manual rollup configuration. Produces
`dist/index.mjs`, `dist/index.cjs`, and `dist/index.d.ts` from a single config line.
The exports map in package.json then lets bundlers and Node pick the right format.

**Alternatives considered:**
- Raw tsc: doesn't handle CJS/ESM split without extra tooling.
- esbuild directly: more control but more configuration — tsup wraps esbuild anyway.
- Rollup: mature but verbose config for a simple library.

---

## 2026-04-20: Node built-in test runner over Jest/Vitest

**Decision:** Use `node --test` (Node built-in) instead of Jest or Vitest.

**Why:** No additional dependency. The built-in runner handles async, assertions, and
subtests for the test surface this library has. Jest and Vitest add hundreds of KB
and a transform step for no benefit in a zero-framework library.

**Constraint:** Requires Node 20+. The CI matrix starts at Node 20, so this is fine.
