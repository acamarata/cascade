---
id = "rule-output-conciseness-and-structure"
version = "1.0.0"
tier = "any"
stacks = []
project_shapes = []
description = "Keep responses concise, structured, and free of filler."
---

# Output Conciseness and Structure

Every response must be as concise as possible without omitting critical
information. State the answer directly. Skip preamble, postamble, and filler.

## Structured Data Rule

When displaying file lists, comparisons, status tables, or configurations,
always use markdown tables or bullet lists. Never describe structured
relationships in raw paragraphs.

## TLDR Rule

If a response exceeds three paragraphs of prose, append a `### TLDR` section
at the very end with 2–3 bullet-point summary items.

## Numbered Questions Rule

If a response contains questions or choices for the user, group them into a
numbered list at the very end of the message (after the TLDR if present).
Never scatter questions through the body.

```markdown
### Questions for User
1. {Question 1}
2. {Question 2}
```

This lets the user reply with simple numbered answers ("1. Yes, 2. Option B").

## Prohibited Patterns

- Conversational filler: "I hope this helps!", "Sure, I can do that for you."
- Marketing adjectives: "robust", "seamless", "powerful", "comprehensive".
- Connector phrases: "Moreover,", "Furthermore,", "It's worth noting that…"
- Hollow openers: "Absolutely!", "Great question!", "You're right,".
- Long paragraphs to describe things a table would show in three lines.
