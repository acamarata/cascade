---
id = "solo-developer"
version = "1.0.0"
tier = "any"
stacks = []
project_shapes = ["solo-developer"]
description = "One person writes, reviews, and ships everything. Optimize for speed, not ceremony."
---

# Solo Developer

One person owns the full stack: architecture, code, review, and deployment.
The process is intentionally lean. Ceremony that exists only to coordinate
between people has no place here.

## Project Structure Expectations

Standard single-repo layout for the chosen stack. No governance artifacts
(CODEOWNERS, CONTRIBUTING.md, RFC templates) unless the project transitions
to open-source. No branching strategy beyond `main` + short-lived feature
branches.

Keep the structure simple enough that any file is findable in under ten seconds.
Avoid deep nesting.

## Decision Norms

Decisions happen fast and in private. Major architectural pivots get a one-line
note in `decisions.md` (or equivalent) so future-you has context.
No RFC, no public comment period, no quorum.

When genuinely uncertain between two approaches, write a single short note
listing the trade-offs, pick one, and move on. Avoid analysis paralysis.

## Code Review Conventions

Self-review only. The practical gate is a passing test suite and type-checker.
For consequential changes (data schema, public API), sleep on it before pushing —
a second reading the next morning catches most mistakes that peer review would find.

No blocked PRs. No approval requirements. CI green = ship.

## Release Cadence

Release when it's ready. No sprints, no milestones, no forced cadence.
Patch and minor releases can ship same-day. Major releases (breaking changes,
significant new features) get a brief changelog entry.

Tag releases in git so rollback is always possible.

## Documentation Expectations

A README that answers: what is this, how do I install it, how do I use it.
Keep it current. Beyond that, document only things that are non-obvious or
that you've gotten wrong before (write it in a `NOTES.md` or inline comments).

No wiki, no docs site, no API reference — unless the project has external users.

## Dependency Philosophy

Minimize dependencies. Every dep you add is one more thing to update, one more
surface for breaking changes. Build small utilities yourself; pull in deps only
for heavy lifting (cryptography, parsing, networking).

Review every new dep's license before adding it. Prefer MIT/BSD/Apache.
