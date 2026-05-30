# PRC — Cursor Integration Example

**Tier:** PRC (Per-Repo Cascade)
**Tool:** Cursor (Tier 2 integration)
**Inherits from:** GCI → APC → PPC

This example shows Cascade setup for a project using Cursor.

---

## How Cascade integrates with Cursor

Cursor reads `.cursorrules` at the repo root for AI instructions. Cascade manages
this file by deriving it from your `CASCADE.md`.

Two modes are available:

**Derived-file mode (recommended):** Cascade writes `.cursorrules` from your
`CASCADE.md` on every save. The derived file is committed to git so Cursor can
always find it, even without Cascade installed.

**Symlink mode:** Cascade creates `.cursorrules` as a symlink to CASCADE.md.
Works for single-user setups. Does not work on Windows.

To set up derived-file mode:
```bash
cascade init --tool cursor --mode derived
```

---

## Cursor workspace scope

Cursor respects `.cursorrules` at the workspace root. If your project is a
monorepo with multiple packages, place this file at the workspace root and keep
repo-specific rules brief (Cursor has a context window budget for rules).

Keep `.cursorrules` under 2,000 tokens. Cascade truncates the derived output
to fit if needed, prioritizing the most specific (innermost) rules.

---

## Repo conventions

Replace this section with your actual conventions.

### Language and tooling

- **Language:** TypeScript (strict mode)
- **Package manager:** pnpm
- **Build:** tsup

### Before opening a PR

- Tests pass: `pnpm test`
- Typecheck: `pnpm run typecheck`
- Lint: `pnpm run lint`

---

## Memory

Cursor does not read memory files directly. Keep your `.cascade/memory/` files
as reference for your own sessions. Key decisions are summarized in `.cursorrules`
via the derived output so Cursor has context.
