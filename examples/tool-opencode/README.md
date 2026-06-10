# OpenCode Integration Example

A minimal Cascade setup for projects using OpenCode.

## What this gives you

- `CASCADE.md` as the single source of truth for your repo's AI instructions
- Cascade derives `AGENTS.md` from it automatically
- Global, project, and repo rules compose automatically
- Mode-specific instruction overlays (Plan mode, Build mode) without duplication

## Setup

```bash
# 1. Install Cascade
curl -fsSL https://cascade.dev/install.sh | bash

# 2. Initialize in your repo
cd /path/to/your/repo
cascade init --tool opencode

# 3. Start editing
cascade edit
```

## File structure created

```
{repo}/
  CASCADE.md              # Your repo-level rules (edit this)
  AGENTS.md               # Derived — do not edit directly
  .cascade/
    memory/
      decisions.md
      lessons.md
      patterns.md
    modes/
      plan.md             # Optional: Plan-mode-specific additions
      build.md            # Optional: Build-mode-specific additions
```

## Mode templates

OpenCode Plan and Build modes can have additional rules that only apply in that mode.
Create `.cascade/modes/plan.md` and `.cascade/modes/build.md` as needed. Cascade
merges them into the right sections of `AGENTS.md`.

Example `modes/plan.md`:
```markdown
When in Plan mode, before writing any plan:
1. Read `.cascade/memory/decisions.md`
2. Run a gap scan against the current FEATURES.md
3. Identify the three largest risks in the proposed work
```

## Updating rules

Edit `CASCADE.md`. Cascade rewrites `AGENTS.md` automatically. OpenCode picks
up the change on its next prompt.

## Further reading

- [Tutorial: Your First Cascade](../tutorial-your-first-cascade.md)
- [Tutorial: Multi-Agent Workflow](../tutorial-multi-agent-workflow.md)
- [Wiki: Integration-OpenCode](https://github.com/acamarata/cascade/wiki/Integration-OpenCode)
