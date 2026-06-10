# Stack Template: React + Vite (SPA)

**Tier:** APC · **Stack:** React 18+ with Vite · **Language:** TypeScript strict

## Idiomatic Layout

```
src/
  components/           # Reusable UI building blocks
    ui/                 # Primitive wrappers (Radix/shadcn)
  hooks/                # Custom React hooks
  lib/                  # Domain logic, utilities
  utils/                # Pure functions, no side effects
  services/             # External API clients, HTTP layer
  types/                # Shared TypeScript interfaces/types
  config/               # Runtime config from env vars
  pages/                # Route-level page components (React Router)
  store/                # Global state (Zustand/Jotai)
  __tests__/            # Or *.test.ts colocated
public/                 # Static assets
.cascade/               # AI working memory (gitignored)
```

## Modular Coding Patterns

- Container/presentational split: data-fetching components separate from display components
- TanStack Query for all server state; no raw fetch in components
- Zustand slices for UI state; one slice per domain
- Hooks extract stateful logic reused across 2+ components
- Services layer is the only place that calls external APIs

## Key Commands

```bash
pnpm dev            # Vite dev server with HMR
pnpm build          # Production bundle (Rollup)
pnpm preview        # Preview production build locally
pnpm lint           # ESLint
pnpm test           # Vitest
pnpm typecheck      # tsc --noEmit
pnpm format         # Prettier
```

## Engineering Rules

- `vite.config.ts`: path aliases for `@/components`, `@/lib`, `@/hooks`
- `tsconfig.json`: `"strict": true`, `"moduleResolution": "bundler"`
- Environment: `VITE_*` prefix for client vars; validated in `config/env.ts` with zod
- File ceiling: components ≤200 lines, services ≤300 lines; split beyond limits
- Tailwind CSS for styling; no inline styles except dynamic values

## Cross-Refs

- `.cascade/rules/frontend-stack-selection.md`
- `.cascade/rules/frontend-state-management.md`
- `.cascade/rules/engineering-excellence.md`
