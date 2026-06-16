---
id = "rule-version-lock"
version = "1.0.0"
tier = "any"
stacks = []
project_shapes = []
description = "Version bumps in any package manifest require an approved release plan before execution."
---

# Version Lock

Version numbers in package manifests are release artifacts, not development
bookkeeping. Bumping a version has real consequences for users who depend on
semantic versioning to manage their own upgrades.

## The Rule

Do not change the `version` field in any package manifest
(`package.json`, `Cargo.toml`, `pyproject.toml`, `pubspec.yaml`, `*.gemspec`,
or equivalent) without an approved release plan in the current session.

An approved release plan must name:
- The exact new version string.
- The version bump type (patch / minor / major) and the reason it applies.
- A confirmation that the changelog is updated with the changes being shipped.
- A confirmation that the pre-publish checklist has been run.

## Automatic Authorization During Active Builds

During an active build phase, **patch-only** bumps are auto-authorized when:
- All tests pass.
- The changelog entry is written.
- The change being shipped is a bug fix, internal refactor, or documentation
  update — no new public API, no breaking change.

Minor and major bumps always require explicit instruction from the user,
regardless of build phase.

## Semver Guidance

| Change type | Version bump |
|---|---|
| Bug fix, internal refactor, docs only | `patch` (1.0.0 → 1.0.1) |
| New exported symbol, new option, backward-compatible | `minor` (1.0.0 → 1.1.0) |
| Removed export, changed signature, breaking behavior | `major` (1.0.0 → 2.0.0) |

When in doubt, choose the higher bump type. Under-bumping misleads users.

## Pre-Publish Gate

Before any publish command runs:
1. Tests pass (`pnpm test` / `cargo test` / equivalent).
2. Type checking passes.
3. Pack output contains expected files and zero warnings.
4. Changelog entry exists for the version being published.
5. User has given explicit approval in this session.
