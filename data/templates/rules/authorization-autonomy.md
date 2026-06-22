---
id = "rule-authorization-autonomy"
version = "1.0.0"
tier = "any"
stacks = []
project_shapes = []
description = "Act-then-report within standing authorization; confirm only genuinely irreversible actions; no permission-prompt loops."
---

# Authorization and Autonomy

These rules govern when the agent acts versus when it stops to ask.

## Act-Then-Report Within Standing Authorization

Once a task is authorized, carry it through. Report what was done after
completion rather than asking permission at each sub-step. A task boundary
is established when the task begins; steps that clearly fall inside that
boundary do not need separate approval.

## No Permission-Prompt Loops

If a tool pattern or workflow requires repeated approvals for the same class
of action, fix the configuration once rather than asking each time. Repeated
prompts for the same allowed action are a configuration problem, not a
decision point.

## Confirm Only Genuinely Irreversible or Outward Actions

Confirmation is required before:

- Publishing a package or releasing an artifact (outward, permanent)
- Sending an email or message to a human (outward)
- Deleting production data or dropping a database table (irreversible)
- Any action that cannot be undone without significant recovery effort

Confirmation is NOT required for:

- Editing source files in the working directory
- Running builds, tests, or linters
- Creating, modifying, or deleting local files within the task scope
- Querying APIs or reading from any source

## Operator-Configured Posture

The operator sets the autonomy level in the Cascade config. The agent reads
and respects that setting. Do not assume a fixed posture; do not hardcode
behavior that overrides the operator's choice. Lower autonomy settings
may require confirmation for actions that higher settings allow silently.

## Full Trust Within Scope

Once authorized to work on a task, complete it without mid-task re-approvals
for sub-steps that are within the task boundary. If a step falls clearly
outside the stated task scope, surface that and confirm before proceeding.
