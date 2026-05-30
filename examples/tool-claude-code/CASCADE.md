# PRC — Claude Code Integration Example

**Tier:** PRC (Per-Repo Cascade)
**Tool:** Claude Code (Tier 1 integration)
**Inherits from:** GCI → APC → PPC

This example shows the minimum Cascade setup for a project using Claude Code.
Copy and adapt it for your repo.

---

## How Cascade integrates with Claude Code

Cascade manages your `CLAUDE.md` file. You write rules in `CASCADE.md` (this file).
Cascade derives `CLAUDE.md` from it automatically on save.

The full instruction path Claude Code reads:

```
~/.claude/CLAUDE.md          (GCI — your global rules)
~/.cascade/CASCADE.md        (APC — all coding projects, if you use one)
{project}/.cascade/CASCADE.md (PPC — project-level rules)
{repo}/CASCADE.md            → derived as CLAUDE.md
```

To set this up:
```bash
cascade init --tool claude-code
```

That creates the symlinks and starts the file-watch daemon.

---

## Session start protocol

At the start of every Claude Code session in this repo:

1. Read `.cascade/memory/decisions.md` — understand architectural choices made
2. Read `.cascade/memory/patterns.md` — understand codebase conventions
3. Check `.cascade/tasks/active.md` if it exists — pick up in-progress work
4. Check `.cascade/inbox/` for incoming messages from other sessions

---

## Repo conventions

Replace this section with your actual repo conventions.

### Language and tooling

- **Language:** TypeScript (strict mode)
- **Package manager:** pnpm
- **Build:** tsup
- **Test:** `node --test`

### File organization

```
src/           # Source code
  components/  # React components (if applicable)
  hooks/       # Custom hooks
  lib/         # Domain logic
  utils/       # Pure utility functions
  types/       # Shared TypeScript types
test/          # Tests, mirroring src/ structure
```

### Before committing

- All tests pass: `pnpm test`
- Typecheck passes: `pnpm run typecheck`
- Lint clean: `pnpm run lint`
- No `TODO` or `FIXME` left in modified files

---

## Memory

Maintain memory files in `.cascade/memory/`:
- `decisions.md` — why non-obvious choices were made
- `lessons.md` — gotchas and mistakes to avoid
- `patterns.md` — codebase conventions

Update them as you work. They are the primary mechanism for keeping knowledge across sessions.
