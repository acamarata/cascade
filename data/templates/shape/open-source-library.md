---
id = "open-source-library"
version = "1.0.0"
tier = "any"
stacks = []
project_shapes = ["open-source-library"]
description = "A publicly available library with external contributors, published to a package registry."
---

# Open-Source Library

A library published for anyone to use. External contributors submit issues and PRs.
The maintainer's job is to keep the project healthy: clear contribution path,
stable public API, and trustworthy releases.

## Project Structure Expectations

Standard single-repo or monorepo layout. Must include: `README.md`, `LICENSE`,
`CHANGELOG.md`, `CONTRIBUTING.md`, and a code of conduct (`CODE_OF_CONDUCT.md`).

The public API surface must be clearly separated from internal implementation.
Exported symbols are documented; unexported internals are not part of the contract.

Issue and PR templates (`.github/ISSUE_TEMPLATE/`, `.github/PULL_REQUEST_TEMPLATE.md`)
reduce back-and-forth with contributors.

## Decision Norms

Major API changes and breaking releases warrant a public discussion (GitHub
Discussions, mailing list, or issue thread) before the decision is final. Give
the community time to raise concerns.

Breaking changes require a migration guide in the release notes. Deprecation
before removal is the default; immediate removal is acceptable only for security
or correctness fixes.

## Code Review Conventions

External PRs need at minimum one maintainer review. The review should be kind
and educational: link to docs, explain the reasoning behind a requested change.
Assume good faith.

Automated checks (CI, formatter, type-checker) catch mechanical issues so reviews
can focus on design and correctness.

Maintain a clear policy on what PRs you will and won't accept (scope, quality bar,
test requirements). State it in `CONTRIBUTING.md` so contributors aren't surprised.

## Release Cadence

Follow semantic versioning strictly. Patch releases for bug fixes can go out
immediately. Minor releases batch new features. Major releases for breaking changes
need a migration guide.

Publish a pre-release (alpha/beta/rc) for major versions so early adopters can
test before the stable tag.

## Documentation Expectations

Complete API reference (auto-generated from code comments where possible).
A quickstart guide in the README. A migration guide for every major version.
`CHANGELOG.md` updated on every release.

Consider a dedicated docs site (e.g. generated with a static site tool) when the
API surface grows beyond two screens of README.

## Dependency Philosophy

Each runtime dependency becomes a transitive dependency for every downstream user.
Minimize runtime deps aggressively. For a library, this is more important than
for an application.

Prefer peer dependencies for host library adapters. Never bundle a large dep that
users are likely to already have.

All deps must carry a permissive license (MIT, BSD, Apache). No copyleft runtime
deps in a library distributed under a permissive license.
