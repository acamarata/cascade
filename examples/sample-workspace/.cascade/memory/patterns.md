# Codebase Patterns

Conventions specific to this repo. Read before implementing anything new.

---

## Error handling

This library never throws for invalid input unless the input is structurally wrong
(wrong type, missing required field). Out-of-range values return a typed error object:

```typescript
type Result<T> = { ok: true; value: T } | { ok: false; error: string };
```

Callers check `result.ok` before using `result.value`. This keeps the API usable in
contexts where try/catch is awkward (React event handlers, Promise chains, etc.).

---

## Exports

Only `src/index.ts` exports are public API. Everything else is internal.

Do not export types that are only used internally. If a type needs to cross a function
boundary inside the library, keep it in the narrowest file scope that works.

Named exports only — no default exports. This makes tree-shaking predictable and
prevents the `import Foo from` vs `import { Foo } from` inconsistency.

---

## File naming

- TypeScript source: `kebab-case.ts`
- Test files: `kebab-case.test.ts` (in `test/`, mirroring `src/`)
- No `.spec.ts`, no `.test.js`

---

## Comments

Function-level JSDoc for every exported symbol. Format:

```typescript
/**
 * Brief one-line description of what this does.
 *
 * @param input - Description of the parameter.
 * @returns Description of the return value.
 * @throws {TypeError} When input is not a string. (Include only if actually thrown.)
 *
 * @example
 * const result = myFunction("example");
 */
```

Inline comments explain why, not what. If the code needs a comment to explain what it
does, rewrite the code to be self-explanatory first.

---

## Versioning

Public API follows semantic versioning. Breaking changes to any exported symbol
require a major version bump, even if the change looks "minor" in terms of lines changed.

When in doubt whether a change is breaking, check: can existing callers continue to
use the old call signature without modification? If yes, it is not breaking.
