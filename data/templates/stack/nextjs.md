---
id = "nextjs"
version = "1.0.0"
tier = "any"
stacks = ["nextjs"]
project_shapes = []
description = "Next.js App Router conventions: layout, testing, build tooling, lint, and common pitfalls."
---

## File Layout

Use the App Router layout. Keep `app/` for routes; shared UI lives in `components/`; server-only logic in `lib/server/`; client helpers in `lib/client/`. Never put server-only imports (e.g. `fs`, `db`) inside files under `components/`.

```
app/
  layout.tsx          # root layout — fonts, global providers
  page.tsx            # homepage Server Component
  (auth)/             # route group — shared layout without URL segment
    login/page.tsx
  api/                # Route Handlers
    health/route.ts
components/
  ui/                 # atomic, reusable — no data fetching
  features/           # feature-scoped composites
lib/
  server/             # never imported from client components
  client/             # safe to import anywhere
  utils/
public/
```

Mark every component with `"use client"` only when it needs browser APIs, event handlers, or React state. Prefer Server Components by default.

## Build Tooling

```bash
pnpm dev          # Next.js dev server with Turbopack (Next {{NEXTJS_VERSION}}+)
pnpm build        # production build — must pass before merge
pnpm start        # serve production build locally
pnpm lint         # ESLint via next lint
pnpm typecheck    # tsc --noEmit (no transpiling)
```

`next.config.ts` (TypeScript config file, not `.mjs`). Pin `{{NEXTJS_VERSION}}` in `package.json`. Use `pnpm` only — never npm or yarn.

## Testing Convention

Co-locate unit tests next to the source file: `button.test.tsx` beside `button.tsx`. Integration and E2E tests live in `tests/`.

```bash
pnpm test              # Vitest (unit + integration)
pnpm test:e2e          # Playwright E2E
pnpm test:coverage     # Vitest coverage report
```

Use Vitest (not Jest) for unit tests — it handles ESM natively. Use `@testing-library/react` for component tests. Never snapshot-test layout components; they produce brittle churn.

For Route Handlers, use `@next/test` or a plain `fetch` against `http://localhost:PORT` in integration tests — do not mock `NextRequest` by hand.

## Lint & Format

```bash
pnpm lint          # next lint (ESLint + Next.js rules)
pnpm format        # prettier --write .
pnpm format:check  # CI gate
```

`.eslintrc` must extend `next/core-web-vitals`. Add `plugin:@typescript-eslint/strict`. Never disable `@typescript-eslint/no-explicit-any` project-wide — fix the types instead.

Prettier config: `semi: true`, `singleQuote: true`, `trailingComma: "all"`, `printWidth: 100`. Commit a `.prettierrc` so all editors agree.

## Common Pitfalls

- **Mixing server and client imports.** Importing a server-only module (prisma, fs) inside a `"use client"` component crashes at runtime, not build time. Use `server-only` package as a guardrail.
- **`useSearchParams` without Suspense.** Next.js requires `useSearchParams()` to be wrapped in `<Suspense>` or the build will error.
- **Stale cache after `fetch`.** `fetch` in Server Components is cached by default. Pass `{ cache: "no-store" }` or `{ next: { revalidate: N } }` explicitly — do not rely on the default changing across Next.js versions.
- **Dynamic imports with SSR.** `next/dynamic` with `{ ssr: false }` skips SSR. Use it only for truly browser-only components; overusing it defeats the performance benefit of the App Router.
- **`app/` vs `pages/`.** Do not mix App Router and Pages Router in the same project unless you are migrating incrementally. Decide once, commit to it.

## Performance Notes

Enable `output: "standalone"` in `next.config.ts` for containerised deployments — it bundles only what is needed.

Analyse bundle size: `ANALYZE=true pnpm build` (requires `@next/bundle-analyzer`). Target a First Contentful Paint under 1.5 s on a 4G connection.

Use `next/image` for all images. Never use `<img>` directly — it skips optimisation and lazy-loading.

Server Component data fetching is parallel by default; wrap independent `async` calls in `Promise.all` only when they are truly independent and both are needed before render.
