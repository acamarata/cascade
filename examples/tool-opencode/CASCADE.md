# PRC — OpenCode Integration Example

**Tier:** PRC (Per-Repo Cascade)
**Tool:** OpenCode (Tier 1 integration)
**Inherits from:** GCI → APC → PPC

This example shows the minimum Cascade setup for a project using OpenCode.
Copy and adapt it for your repo.

---

## How Cascade integrates with OpenCode

Cascade manages your `AGENTS.md` file. You write rules in `CASCADE.md` (this file).
Cascade derives `AGENTS.md` from it automatically on save.

OpenCode reads `AGENTS.md` as its instruction source. The composition works the same
way as any Tier 1 integration: global cascade composes with project-level and
repo-level instructions.

To set this up:
```bash
cascade init --tool opencode
```

---

## OpenCode-specific conventions

### Mode templates

OpenCode supports named modes (Plan mode, Build mode, etc.). You can configure
mode-specific instruction overrides in `.cascade/modes/`:

```
.cascade/
  modes/
    plan.md     # Additional instructions active only in Plan mode
    build.md    # Additional instructions active only in Build mode
```

Cascade merges these into the appropriate sections of `AGENTS.md`.

### Agent configuration

OpenCode's `~/.config/opencode/opencode.json` configures providers and default models.
That file is managed separately — Cascade does not touch it. Your `CASCADE.md` rules
layer on top of whatever models are configured there.

---

## Session start protocol

At the start of every OpenCode session in this repo:

1. Read `.cascade/memory/decisions.md`
2. Read `.cascade/memory/patterns.md`
3. Check `.cascade/tasks/active.md` if present
4. Check `.cascade/inbox/` for messages

---

## Repo conventions

Replace this section with your actual conventions.

### Language and tooling

- **Language:** TypeScript (strict mode)
- **Package manager:** pnpm
- **Build:** tsup
- **Test:** `node --test`

### Tier assignment for tasks

When dispatching subagents, use these tier assignments:
- T3 (cheap/fast): classification, triage, file moves, simple edits
- T2 (default): implementation, research, doc writes, code review
- T1 (decision-making): architecture decisions, security review, final acceptance

---

## Memory

Maintain in `.cascade/memory/`:
- `decisions.md` — architectural decisions and rationale
- `lessons.md` — gotchas and mistakes to avoid
- `patterns.md` — codebase conventions

Update as you work.
