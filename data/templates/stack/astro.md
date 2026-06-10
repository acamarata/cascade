---
id = "astro"
version = "1.0.0"
tier = "any"
stacks = ["astro"]
project_shapes = []
description = "Astro 4+ conventions: islands, MDX, TypeScript, content collections, build tooling, and common pitfalls."
---

## File Layout

```
src/
  components/
    ui/           # fully static — no framework imports
    islands/      # interactive island components (React, Vue, etc.)
  layouts/        # base layout templates wrapping pages
  pages/          # file-based routing (.astro, .md, .mdx, .ts)
    api/          # server endpoints (.ts files)
  content/        # content collections (blog, docs, etc.)
    config.ts     # collection schemas
  lib/            # utilities — no component code here
  styles/         # global CSS
public/           # static assets served as-is (no Astro processing)
astro.config.mjs  # Astro config (stays .mjs — Astro convention)
```

Everything in `src/pages/` becomes a route. Files under `src/content/` are only accessible via the Content Collections API — they are never routes on their own.

## Build Tooling

```bash
pnpm dev          # Astro dev server with HMR
pnpm build        # full static build to dist/
pnpm preview      # serve dist/ locally
pnpm check        # astro check (type-check + diagnostics)
pnpm typecheck    # tsc --noEmit (supplementary)
```

Pin `{{ASTRO_VERSION}}` in `package.json`. Enable `output: "static"` for purely static sites; use `output: "server"` with an adapter (e.g. `@astrojs/node`, `@astrojs/vercel`) only when SSR is needed. Do not mix static and server output without a clear reason.

## Testing Convention

```bash
pnpm test          # Vitest unit tests
pnpm test:e2e      # Playwright E2E
```

Astro components (`.astro` files) are not Vitest-testable directly — extract pure logic into `.ts` files and test those. Test interactive island components with `@testing-library/react` or the appropriate framework library.

For content collections, write unit tests against the parsed data shape using Vitest. Do not rely on build-time errors alone to catch malformed content.

## Islands Architecture

Each island is a React (or other framework) component with an explicit `client:*` directive:

| Directive | When to use |
|---|---|
| `client:load` | Visible above the fold, needs JS on page load |
| `client:idle` | Below the fold, hydrate after page is interactive |
| `client:visible` | Hydrate only when scrolled into view |
| `client:only="react"` | Skip SSR entirely (browser-only component) |

Default to `client:visible` for below-fold interactive content. Avoid `client:load` unless the component must be interactive immediately. Every `client:*` component adds to the JavaScript bundle — treat them as a cost.

## Content Collections

Define collection schemas in `src/content/config.ts` using `zod`. Every collection entry is type-checked at build time.

```ts
// src/content/config.ts
import { defineCollection, z } from "astro:content";

const blog = defineCollection({
  schema: z.object({
    title: z.string(),
    pubDate: z.date(),
    tags: z.array(z.string()).optional(),
  }),
});

export const collections = { blog };
```

Use `getCollection("blog")` in pages to fetch entries. Never read content files with `fs.readFile` — always go through the collections API.

## Lint & Format

```bash
pnpm lint          # ESLint with eslint-plugin-astro
pnpm format        # prettier --write . (Prettier supports .astro via plugin)
pnpm format:check  # CI gate
```

Install `eslint-plugin-astro` and `prettier-plugin-astro`. Add both to the ESLint config and Prettier config respectively. Without `prettier-plugin-astro`, Prettier will corrupt `.astro` files.

## Common Pitfalls

- **Importing React in `.astro` files.** You do not need `import React from "react"` in `.astro` files — Astro handles JSX transforms for `.astro` files differently. Only island `.tsx` components need the React import (and only for older JSX transforms).
- **`Astro.props` not typed.** Define `Props` as a TypeScript interface and use `const { x }: Props = Astro.props`. Without this, props are `any`.
- **MDX frontmatter schema mismatch.** If a content collection has a strict Zod schema, a missing or wrong-typed frontmatter field throws at build time, not at dev time. Validate frontmatter locally before pushing.
- **`public/` vs `src/assets/`.** Files in `public/` are served as-is (no optimisation). Files in `src/assets/` are processed by Vite (hashed, optimised). Use `src/assets/` for images you want `<Image />` to optimise.
- **View Transitions and island state.** With `<ViewTransitions />` enabled, island component state is not preserved across navigations by default. Use `transition:persist` on components that should keep their state.

## Performance Notes

Astro ships zero JS by default. Every `client:*` directive is a deliberate choice to add JS. Audit island count before shipping.

Use `<Image />` from `astro:assets` for all images — it generates responsive `srcset` and WebP variants automatically. Never use `<img>` tags for content images.

Run `pnpm build && pnpm preview` and check the Lighthouse score before each release. An Astro site should score 95+ on performance for static pages.
