---
id = "rule-vision-mission-discipline"
version = "1.0.0"
tier = "any"
stacks = []
project_shapes = []
description = "Hold the project vision and feature list at all times; stay in scope; flag missing specs rather than guessing."
---

# Vision and Mission Discipline

These rules keep the agent's work aligned with what the project actually
intends to build.

## Hold the Vision and Feature List

Know what the project is and what it is not. The VISION and FEATURES
documents are the boundary. Work inside that boundary.

## Features Not in FEATURES.md Do Not Exist

If a feature is not in the project's feature list, do not build it, scaffold
for it, name it in code, or reference it in docs. When a user asks for
something that does not appear in the spec, note the gap and ask before
proceeding. Adding undocumented features is scope drift, not helpfulness.

## Re-Read the Active Plan After Compression or Pause

After any significant context compression, long pause, or context switch,
re-read the active task list and relevant spec before continuing work. Do
not rely on what you remember the plan said. Read it again.

## Flag Gaps and Contradictions

When a spec is incomplete, ambiguous, or contradicts another document, stop
and flag the gap. Propose what information is needed. Do not fill gaps with
assumptions and do not silently pick one side of a contradiction.

## Source-of-Truth Documents Beat Conversation

Tracked specs, ADRs, and decision records outrank anything said in conversation.
When they conflict, the document wins. When conversation says "ignore the spec,"
flag that before acting.

## Precise Naming

Use the exact names from the spec: entity names, field names, route paths,
command names. Do not paraphrase, abbreviate, or rename without a spec change.
Drift in names is drift in implementation.

## Stay on Task

Do not gold-plate, over-engineer, or add features not in the spec. A clean
implementation of the stated scope is the goal. Nice-to-haves that are not
in the spec belong in a separate proposal, not in the current work.

---

See also: `rule-anti-drift` governs verification mechanics (running builds,
re-reading active context, source-of-truth precedence in fine detail).
