# Stack Template: Next.js (App Router)

**Tier:** APC · **Stack:** Next.js 14+ with App Router · **Language:** TypeScript strict

## Idiomatic Layout

```
app/                    # Routes, layouts, pages (file-based routing)
  (marketing)/          # Route groups — no URL segment
  (app)/                # Authenticated app shell
  api/                  # Route handlers
components/             # Reusable UI (atomic + composite)
  ui/                   # Shadcn/Radix primitives
hooks/                  # Client-side stateful logic
lib/                    # Domain business logic, server utilities
utils/                  # Pure functions, no side effects
services/               # External API clients
types/                  # Shared TypeScript types
config/                 # Runtime config loaded from env
__tests__/              # Or *.test.ts colocated with source
.cascade/               # AI working memory (gitignored)
```

## Modular Coding Patterns

- One route segment = one directory; layout.tsx + page.tsx + loading.tsx + error.tsx per segment
- Server components by default; add `"use client"` only when browser APIs or interactivity required
- Data fetching in server components via async/await; no useEffect for data
- Shared data access layer in `lib/` — never fetch directly inside components
- State: TanStack Query for server state, Zustand/Jotai for UI state, React Hook Form for forms

## Key Commands

```bash
pnpm dev            # Development server (Turbopack)
pnpm build          # Production build
pnpm start          # Start production server
pnpm lint           # ESLint
pnpm test           # Jest / Vitest
pnpm typecheck      # tsc --noEmit
```

## Engineering Rules

- `tsconfig.json`: `"strict": true`, `"moduleResolution": "bundler"`
- Environment variables: `NEXT_PUBLIC_*` for client-exposed; server-only vars in `lib/env.ts` validated with zod
- Co-located tests preferred: `ComponentName.test.tsx` beside `ComponentName.tsx`
- File ceiling: components ≤200 lines, lib/utils ≤300 lines; split by domain beyond limits
- `.cascade/docs/MASTER-ROUTES.md` tracks every app/ route and api/ handler

## Cross-Refs

- `.cascade/rules/frontend-stack-selection.md`
- `.cascade/rules/engineering-excellence.md`
- `.cascade/rules/master-lists-protocol.md`
