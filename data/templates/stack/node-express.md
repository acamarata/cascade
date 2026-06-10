---
id = "node-express"
version = "1.0.0"
tier = "any"
stacks = ["node", "express", "typescript"]
project_shapes = []
description = "Node/Express service: TypeScript, Vitest, ESLint/Prettier"
---

# CASCADE Instructions — Node / Express Service

> Stack: Node 20+ · Express 4/5 · TypeScript strict · Vitest · ESLint / Prettier
> Tier: any (typically PRC or PAC)

Use `{{project_name}}` for the package name, `{{port}}` for the default port (e.g. `3000`), and `{{node_version}}` for the minimum Node version (e.g. `20`).

---

## Module / Package Layout

```
{{project_name}}/
├── src/
│   ├── app.ts                   # Express app factory (no listen — easier to test)
│   ├── server.ts                # entry point: creates app, starts listener
│   ├── config.ts                # env-var config via zod or envalid
│   ├── middleware/
│   │   └── error-handler.ts
│   ├── routes/
│   │   └── health.ts
│   └── services/                # business logic, no Express types here
├── tests/
│   ├── app.test.ts
│   └── routes/
│       └── health.test.ts
├── dist/                        # compiled output (gitignored)
├── package.json
├── tsconfig.json
├── .eslintrc.cjs
├── .prettierrc
└── .env.example
```

Keep Express request/response types out of `services/`. Services take plain inputs and return plain outputs.

---

## Build & Tooling

```json
{
  "scripts": {
    "build": "tsc --project tsconfig.json",
    "start": "node dist/server.js",
    "dev": "tsx watch src/server.ts",
    "test": "vitest run",
    "test:watch": "vitest",
    "lint": "eslint src --ext .ts",
    "format": "prettier --write src tests",
    "typecheck": "tsc --noEmit"
  },
  "engines": {
    "node": ">={{node_version}}"
  }
}
```

**`tsconfig.json` (strict baseline):**

```json
{
  "compilerOptions": {
    "target": "ES2022",
    "module": "CommonJS",
    "outDir": "dist",
    "rootDir": "src",
    "strict": true,
    "esModuleInterop": true,
    "skipLibCheck": false,
    "forceConsistentCasingInFileNames": true
  },
  "include": ["src"],
  "exclude": ["node_modules", "dist"]
}
```

---

## Testing Convention

Use Vitest with `supertest` for HTTP integration tests.

```typescript
// tests/routes/health.test.ts
import { describe, it, expect } from "vitest";
import request from "supertest";
import { createApp } from "../../src/app";

describe("GET /health", () => {
  const app = createApp();

  it("returns 200 OK", async () => {
    const res = await request(app).get("/health");
    expect(res.status).toBe(200);
    expect(res.body).toMatchObject({ status: "ok" });
  });
});
```

- Split `app.ts` (factory) from `server.ts` (listener) so tests can import the app without starting a port.
- Unit-test services in isolation without a running Express instance.
- Coverage via `vitest --coverage` (v8 provider).

---

## Lint & Format

ESLint with TypeScript plugin + Prettier. They do not overlap: ESLint catches logic issues, Prettier owns formatting.

```bash
# Lint
pnpm lint

# Format (writes in place)
pnpm format

# Format check (CI)
prettier --check src tests
```

`.eslintrc.cjs` minimum:

```js
module.exports = {
  root: true,
  parser: "@typescript-eslint/parser",
  plugins: ["@typescript-eslint"],
  extends: [
    "eslint:recommended",
    "plugin:@typescript-eslint/recommended-type-checked",
  ],
  parserOptions: { project: "./tsconfig.json" },
};
```

---

## Common Pitfalls

- **Separate `app.ts` from `server.ts`.** `app.ts` exports `createApp()` and is importable in tests without side effects. `server.ts` calls `app.listen()`.
- **Typed error middleware.** Express 4 error handlers have 4 arguments `(err, req, res, next)`. If you drop one, Express will not treat it as an error handler.
- **Express 5 async error handling.** Express 5 automatically catches promise rejections in route handlers. Express 4 requires explicit `next(err)` or a `try/catch` wrapper.
- **`strict: true` in tsconfig.** Never relax this. If a library causes type errors, add `@types/*` or narrow with a local declaration file.
- **Environment variables via typed config.** Access `process.env` only in `config.ts`; everywhere else imports from `config.ts`. This makes the type surface explicit and testable.
