---
id = "multi-repo-product"
version = "1.0.0"
tier = "any"
stacks = []
project_shapes = ["multi-repo-product"]
description = "A product built from several independent git repositories under one project umbrella."
---

# Multi-Repo Product

A product where each major concern (backend, frontend, mobile, SDK, docs) lives in
its own git repository. Repos are versioned and released independently.
A project-level directory ties them together without merging their histories.

## Project Structure Expectations

One project folder (e.g. `~/Sites/myproduct/`) contains sub-repos as peer directories.
Each sub-repo is a standalone git repository with its own `package.json` / `Cargo.toml`
/ build config. The project folder itself is NOT a git repo. It may contain:
- `.claude/` — shared AI context and planning docs
- `README.md` — project overview linking to each repo

No cross-repo symlinks. No shared `node_modules` at project root.
Cross-repo logic is shared via published packages or an internal registry, never via
relative `../` paths.

## Decision Norms

Architecture decisions that affect multiple repos require a written ADR before
implementation begins. Single-repo changes that don't affect the shared interface
need no cross-team approval.

Breaking changes to a shared API or SDK require a deprecation period: announce in
the next minor release, remove in the next major. Document the migration path.

## Code Review Conventions

Each repo has its own review process. A PR that touches a shared contract (API shape,
SDK type) notifies owners of dependent repos before merge.

No cross-repo PR is merged without the dependent repos confirming compatibility
(or bumping their own version to absorb the change).

## Release Cadence

Repos release on their own schedules. Coordinated releases (e.g. a new API version
with a matching SDK version) are announced in a release note that links all affected
repo tags.

Maintain a project-level `releases.md` or GitHub milestone that groups coordinated
tags together.

## Documentation Expectations

Each repo has its own `README.md` and `CHANGELOG.md`. The project folder README
links to all repos and shows the current compatibility matrix (which repo versions
work together).

API contracts get a shared spec doc (OpenAPI, GraphQL schema, etc.) committed to
the API repo and referenced from all consumers.

## Dependency Philosophy

Shared runtime logic lives in a published package, not in symlinked `../` source.
Bumping a shared package is a deliberate versioning event, not a side effect.

Pin peer-dependency ranges to majors (`^1.x`), not exact versions, to let
dependants absorb patches without lockstep upgrades.
