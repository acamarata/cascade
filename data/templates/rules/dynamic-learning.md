---
id = "rule-dynamic-learning"
version = "1.0.0"
tier = "any"
stacks = []
project_shapes = []
description = "Capture decisions, lessons, and patterns to memory as you work; tag via the cheap pool; recall before re-deriving; never lose a learning."
---

# Dynamic Learning

Every agent using Cascade must actively build its own memory as it works. Do
not discard discoveries at the end of a task — capture them so they are
available for the next task, the next session, and every other agent in the
same project.

## Capture as You Go

- When you make a significant technical or architectural choice, write it to
  `decisions.md`. Include the rationale and any alternatives you ruled out.
- When you hit a gotcha, bug, or hard-won learning, write it to `lessons.md`.
  Name the pitfall precisely so a future agent will recognise it.
- When you observe a stable convention or idiom that the project uses, write
  it to `patterns.md`. Other agents should be able to discover it without
  reading the whole codebase.

Use `cascade memory capture "<text>"` to classify and route automatically.
The taxonomy classifier determines the correct file; you can override with
`--file <decisions|lessons|patterns>`.

## Tag Every Entry

Every memory entry must include at least one primary category tag:
`#decision`, `#lesson`, or `#pattern`.

Add domain tags where they apply: `#security`, `#performance`, `#api`,
`#testing`, `#docs`, `#dependencies`, `#config`, `#errors`, `#data-model`.

Tags are written automatically by `cascade memory capture`. When writing
directly via `cascade memory write`, include them in the entry heading:

```
## 2026-01-15  #decision #security

Decided to store API keys in the OS keychain rather than .env files.
Rationale: avoids accidental commits; keychain survives .env resets.
```

## Use the Cheap Pool for Classification

Route classification tasks through the `Cheap` lane (GFP free Flash) — it
costs nothing and adds context-aware tagging beyond keyword matching.
`cascade memory capture` does this automatically. If GFP is unavailable,
the rule-based fallback runs transparently; no action needed.

## Recall Before Re-Deriving

Before researching or solving a problem you may have solved before, recall:

```
cascade search "<topic or question>"
cascade memory read decisions
cascade memory read lessons
```

If a relevant entry exists, use it. Never re-derive what is already known.
Recall costs seconds; re-derivation costs minutes and may produce a different
answer.

## Never Lose a Learning

A task is not done until any significant discovery has been written to memory.
If your session ends before you write a learning, a future agent starts from
zero. Treat memory capture as part of the task definition of done, not an
optional extra.

## Scope

This rule applies to every agent tier (T0/T1/T2/T3) and every project shape.
The memory files live in `.cascade/memory/` at the tier nearest to the work:
- Cross-project learnings → GCI tier (`~/.cascade/memory/`)
- Project-wide learnings → PPC tier
- Repo-specific learnings → PRC tier
- Per-app learnings → PAC tier
