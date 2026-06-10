---
id = "personal-npm-package"
version = "1.0.0"
tier = "any"
stacks = []
project_shapes = ["personal-npm-package"]
description = "A single maintainer's public npm package — minimal overhead, high automation."
---

# Personal npm Package

A focused, maintainer-owned npm package published under a personal or org scope.
One person makes all decisions; automation fills the review gap that a team would otherwise cover.

## Project Structure Expectations

Each package lives in its own git repository. No monorepo. Root contains only
`package.json`, `README.md`, `LICENSE`, `CHANGELOG.md`, and `.gitignore`.
Source in `src/`, compiled output in `dist/`, tests co-located with source or
in `test/`. Config files (`tsconfig.json`, `tsup.config.ts`) at root only.

No `docs/`, no `apps/`, no `packages/` subdirectory — those patterns belong to
multi-repo or monorepo shapes.

## Decision Norms

The maintainer decides alone. No RFC process, no issue triage queue.
Major decisions get a one-line commit note explaining why (e.g. "switch to ESM-only:
Node 18 EOL"). Breaking changes require a CHANGELOG entry and a semver major bump;
everything else is a minor or patch at maintainer discretion.

No decision is blocked on external review. Automation (CI, pack-check) is the gate.

## Code Review Conventions

Self-review only. Run the full test suite and type-checker before every push.
Treat the CI green-tick as the final quality gate. For non-trivial changes, wait
a short cool-off period (a day or two) before tagging a release — a second reading
with fresh eyes catches most mistakes.

No PR merges without CI green. No force-push to `main`.

## Release Cadence

Releases happen when work is done, not on a schedule. Patch releases can go out
same-day. Minor releases warrant a brief CHANGELOG paragraph. Major releases get
a migration guide in the README.

Automate releases via CI where possible (`npm publish` from a tagged push).
Never publish manually from a local environment without first verifying CI passed.

## Documentation Expectations

`README.md` must contain: install command, minimal usage example, API surface
overview, and a link to `CHANGELOG.md`. Every exported symbol gets a JSDoc comment.
The CHANGELOG follows Keep a Changelog format.

No separate docs site needed until the API surface exceeds one screen of README.

## Dependency Philosophy

Prefer zero runtime dependencies. Every runtime dep is a transitive dep for all
downstream users. If a dep is only needed for a utility or formatting task, inline
the minimal code instead. Accept a runtime dep only for non-trivial algorithms
(e.g. date math, cryptography, physics).

Dev dependencies (test runner, bundler, type-checker) are unrestricted.

All deps must be MIT, BSD-2, BSD-3, Apache-2.0, ISC, or CC0 licensed.
No GPL, LGPL, or AGPL runtime deps.
