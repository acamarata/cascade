---
id = "react-vite"
version = "1.0.0"
tier = "any"
stacks = ["react-vite"]
project_shapes = []
description = "React 18 + Vite 5 SPA conventions: file layout, testing, build tooling, shadcn/ui, lint, and common pitfalls."
---

## File Layout

Vite projects use a flat `src/` layout. Co-locate related files by feature, not by file type.

```
src/
  components/
    ui/             # shadcn/ui primitives (never edit generated files manually)
    features/       # feature-scoped composites
  hooks/            # custom React hooks
  lib/              # pure utilities, formatters, helpers
  services/         # API calls, SDK wrappers
  types/            # shared TypeScript types and interfaces
  pages/            # top-level route views (thin — delegate to features/)
  App.tsx
  main.tsx
public/
index.html          # Vite entry point — keep minimal
vite.config.ts      # TypeScript config, not .mjs
```

Keep `main.tsx` to provider setup only. Each feature folder owns its own components, hooks, and types — do not share state across features via prop drilling; use context or a state library instead.

## Build Tooling

```bash
pnpm dev          # Vite dev server with HMR
pnpm build        # TypeScript compile + Vite production bundle
pnpm preview      # serve the dist/ build locally
pnpm typecheck    # tsc --noEmit (separate from build)
```

Pin `{{VITE_VERSION}}` and `{{REACT_VERSION}}` in `package.json`. Use `vite.config.ts` with `@vitejs/plugin-react` (SWC variant for faster builds). Set `build.target` explicitly — do not rely on Vite defaults changing.

## Testing Convention

```bash
pnpm test              # Vitest (watch mode in dev)
pnpm test:run          # Vitest CI mode (single pass)
pnpm test:coverage     # Vitest + v8 coverage
pnpm test:e2e          # Playwright
```

Set `environment: "jsdom"` in `vitest.config.ts`. Use `@testing-library/react` for component tests; `@testing-library/user-event` for interactions. Co-locate tests: `Button.test.tsx` beside `Button.tsx`.

Mock fetch and external services in tests — never make real HTTP calls in unit tests. Use `msw` (Mock Service Worker) for integration tests that need realistic API responses.

## Lint & Format

```bash
pnpm lint          # ESLint (eslint-plugin-react, @typescript-eslint)
pnpm lint:fix      # auto-fix
pnpm format        # prettier --write src/
pnpm format:check  # CI gate
```

ESLint config must enable `react-hooks/rules-of-hooks` and `react-hooks/exhaustive-deps`. The exhaustive-deps rule is a warning locally and an error in CI — fix the underlying hook, never add an eslint-disable comment.

Add `plugin:react/jsx-runtime` to avoid the `React` import requirement with the new JSX transform. Set `"jsx": "react-jsx"` in `tsconfig.json`.

## shadcn/ui Conventions

Add components via `npx shadcn@latest add <component>` — never copy-paste from the docs manually. Generated files land in `src/components/ui/`. Treat them as owned source: they are checked in and can be modified. Do not regenerate a component just to undo local changes.

Customise via CSS variables in `src/index.css` (the design tokens pattern). Do not override Tailwind utility classes directly on shadcn components — extend the token layer instead.

## Common Pitfalls

- **Stale closures in `useEffect`.** Always list every value the effect reads in the dependency array. The exhaustive-deps lint rule catches most cases; trust it.
- **`React.StrictMode` double-invoking effects.** Development mode runs effects twice to detect side effects. If your effect behaves incorrectly on double-invoke, the effect has a side-effect bug, not a StrictMode bug.
- **Large bundle from barrel imports.** `import { X } from './components'` with a barrel file causes Vite to load every export. Prefer direct imports or check that Vite's tree-shaking is working with `pnpm build --report`.
- **Missing `key` on list renders.** Use stable, unique ids from data — never array indices for lists that can reorder.
- **TypeScript `any` via `as unknown as X`.** Double-cast is a type safety escape hatch. If you need it, your types are wrong upstream.

## Performance Notes

Use `React.lazy` + `<Suspense>` for route-level code splitting. Each page component should be a lazy import in the router config.

Profile renders in development with the React DevTools Profiler before adding `useMemo` or `useCallback` — premature memoisation adds complexity without benefit. Apply them where the profiler shows actual wasted renders.

Target a Lighthouse performance score of 90+ on the production build. Run `pnpm preview` and measure before shipping.
