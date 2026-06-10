---
id = "pci-default"
version = "1.0.0"
tier = "pci"
stacks = []
project_shapes = []
description = "Vendor-neutral default instructions for the Personal Cascade Instructions tier."
---

# Personal Cascade Instructions (PCI)

This file sits between the global tier (GCI) and the all-projects tier (APC).
It holds instructions specific to personal, non-coding work: communication
style, privacy rules, task routing for personal projects, and conventions that
do not belong inside any single project.

PCI means Personal Cascade Instructions. It is NOT a project inbox.
Per-project inboxes live at `.cascade/inbox/` inside each project directory
and use the shorthand PPCi. Do not conflate the two.

## Personal Ops Scope

Domains covered by this tier (fill in yours):
- Personal finance tracking: {{FINANCE_SCOPE}}
- Health and calendar: {{HEALTH_CALENDAR_SCOPE}}
- Non-work correspondence: {{CORRESPONDENCE_SCOPE}}
- Side projects outside the main dev tree: {{SIDE_PROJECTS}}

Work that falls inside a project listed in APC or PPC belongs in those tiers,
not here.

## Non-Coding Work Conventions

- Task management: {{TASK_TOOL}} (e.g. a local task file, a CLI tool, a notes app)
- Reference storage: {{REFERENCE_LOCATION}}
- Scratch space: {{SCRATCH_PATH}}

Keep non-coding work files out of version-controlled directories unless the
user explicitly asks to track them.

## Communication Tone

All outbound messages written on behalf of the user follow these rules:
- No AI-generated filler phrases or corporate jargon.
- Short paragraphs. One idea per sentence.
- Match the formality level of the recipient and context.
- Redact sensitive personal data (ID numbers, account numbers, dates of birth)
  unless the recipient requires them and has a legitimate need.

Before sending any message to a person (email, SMS, form submission), show the
full rendered text and recipient list. Wait for explicit approval.

## Privacy Rules

- Do not log, cache, or persist personal data beyond the immediate task.
- Do not include personal health, financial, or family information in any
  output that may be stored in a shared location.
- Files in {{PROTECTED_DIRS}} are read-only for reference; never quote their
  contents into outputs that leave this machine.

## Session Conventions

Personal-context work sessions should start by reading the GCI, then this PCI.
They do not need to load project files unless the work is project-related.
