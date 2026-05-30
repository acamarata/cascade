# Claude Code Integration Example

A minimal Cascade setup for projects using Claude Code.

## What this gives you

- `CASCADE.md` as the single source of truth for your repo's AI instructions
- Cascade derives `CLAUDE.md` from it automatically
- Your global rules (`~/.cascade/CASCADE.md`) and project rules compose automatically
- Memory files carry knowledge across sessions without repeating it in every prompt

## Setup

```bash
# 1. Install Cascade
curl -fsSL https://cascade.dev/install.sh | bash

# 2. Initialize in your repo
cd /path/to/your/repo
cascade init --tool claude-code

# 3. Start editing
cascade edit
```

The `cascade edit` command opens CASCADE.md in your editor. Changes are derived to
CLAUDE.md automatically.

## File structure created

```
{repo}/
  CASCADE.md              # Your repo-level rules (edit this)
  CLAUDE.md               # Derived — do not edit directly
  .cascade/
    memory/
      decisions.md        # Technical decisions and rationale
      lessons.md          # Gotchas discovered during development
      patterns.md         # Codebase conventions
```

## Updating rules

Edit `CASCADE.md`. Cascade picks up the change and rewrites `CLAUDE.md` within
a few seconds. Claude Code reads the updated file on its next prompt.

You never edit CLAUDE.md directly.

## Global rules

Put rules that apply to all your projects in `~/.cascade/CASCADE.md`. They compose
automatically with every repo-level CASCADE.md.

Common global rules:
- Your preferred coding style
- Which test frameworks you use
- Your commit message format
- Any AI interaction preferences that apply everywhere

## Further reading

- [The Cascade Concept](https://github.com/acamarata/cascade/wiki/The-Cascade-Concept)
- [Six-Tier Taxonomy](https://github.com/acamarata/cascade/wiki/Six-Tier-Taxonomy)
- [Tutorial: Your First Cascade](../tutorial-your-first-cascade.md)
