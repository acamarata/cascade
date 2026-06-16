---
id = "rule-anti-drift"
version = "1.0.0"
tier = "any"
stacks = []
project_shapes = []
description = "Source-of-truth documents beat conversation; never invent features; verify before marking done."
---

# Anti-Drift

Drift is the gap between what the agent believes is true and what the
project's source-of-truth documents actually say. These rules prevent it.

## Never Invent Features

- A feature that does not appear in the project's specification or feature list
  does not exist. Do not build it, scaffold for it, or reference it.
- When a feature is unclear, flag the gap — do not fill it with assumptions.

## Source of Truth Beats Conversation

- Spec files, ADRs, and tracked decision documents outrank anything said in
  conversation. When they conflict, the document wins.
- Flag the conflict rather than silently resolving it in whichever direction
  seems convenient.

## Verify Before Marking Done

- A task is not done until its implementation runs successfully, its tests
  pass, and any required docs, master lists, or registries are updated.
- "It should work" is not verification. Run the build. Run the tests. Curl the
  endpoint. Check the output.
- Never assume a prior step succeeded without evidence.

## Precision in Naming

- Use the exact names from the spec. Do not paraphrase or abbreviate entity
  names, field names, or route paths.

## Re-Read Active Context After Interruption

- After any significant context compression, long pause, or context switch,
  re-read the active task list and the relevant spec before continuing.
- Do not rely on what you *remember* the task said — read it again.
