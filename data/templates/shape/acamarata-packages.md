---
id = "acamarata-packages"
version = "1.0.0"
tier = "any"
stacks = []
project_shapes = ["acamarata-packages"]
description = "The specific conventions used by the acamarata org for its public npm and pub.dev packages."
---

# acamarata Packages

The concrete practices used by the `acamarata` GitHub org for its portfolio of
public Islamic-utility packages: hijri calendar, prayer times, moon sighting,
qibla direction, and their Dart counterparts.

This shape template is a precise description of the acamarata pattern.
Use it when initializing a new acamarata-org repository.

## Project Structure Expectations

Each package lives in its own independent git repository. No monorepo.
JS/TS repos and their Dart counterparts are separate repos on separate registries
(npm vs pub.dev) with separate version histories.

Root of every JS/TS repo contains only: `package.json`, `README.md`, `CHANGELOG.md`,
`LICENSE` (MIT), `.gitignore`, `tsconfig.json`, `tsup.config.ts`, and `.npmrc` (empty).
No extra config files at root. Source in `src/`, compiled output in `dist/`.

Dart repos follow `dart create` layout: `lib/`, `test/`, `pubspec.yaml`,
`CHANGELOG.md`, `README.md`, `LICENSE`.

## Decision Norms

Packages that share an algorithm (e.g. `nrel-spa` JS and `nrel_spa` Dart) are
independent codebases that share design intent, not source code. Changes to one
do not automatically require a change to the other, but significant algorithm
updates should eventually be mirrored.

`hijri-core` is the foundation for all hijri adapter packages. No calendar logic
belongs in an adapter. Adapters call hijri-core, wrap its output, and add nothing
more.

Dependencies between packages follow a strict DAG. No circular dependencies, ever.

## Code Review Conventions

Self-review is standard. The quality gates are automated: CI must be green,
`npm pack --dry-run` must show zero warnings, type-checker must exit 0, coverage
must meet the target for the package's category (≥90% for foundation packages,
≥85% for plugins, ≥80% for feature packages).

No PR merges to main with failing CI. No force-push to main.

## Release Cadence

Releases happen when work is done, not on a schedule. Every release requires:
- Updated `CHANGELOG.md` entry (Keep a Changelog format)
- Version bump in `package.json` / `pubspec.yaml`
- Git tag matching `v<version>`
- `npm publish` (not `pnpm publish`) for JS packages
- `dart pub publish` for Dart packages
- Explicit user approval before any publish

No automated publishing. Every release is a deliberate act.

When `hijri-core` publishes a major version, all five adapter packages
(`luxon-hijri`, `date-fns-hijri`, `dayjs-hijri-plus`, `moment-hijri-plus`,
`temporal-hijri`) release a coordinated update in the same session.

## Documentation Expectations

Every exported symbol has a JSDoc comment (TS) or dartdoc comment (Dart).
`README.md` includes: install command, minimal usage example, and API overview.
`CHANGELOG.md` is updated on every release.

No generated docs site needed unless the API surface outgrows the README.
Wiki files (`.wiki/` directory) sync to GitHub Wiki via CI for any repo
that has external users.

## Dependency Philosophy

Foundation packages (`hijri-core`, `nrel-spa`, etc.) have zero runtime dependencies.
Plugin packages list the host library as a `peerDependency`, never as a
`dependency`, to avoid bundling the host twice.

All runtime deps must be MIT, BSD-2, BSD-3, Apache-2.0, ISC, or CC0.
No GPL, LGPL, or AGPL runtime deps.

No AI attribution in any version-controlled output. A pre-commit hook enforces
this globally. `cascade` is the one exception (it is in the `ALLOW_AI_TERMS_REPOS`
list because it legitimately discusses AI tooling).
