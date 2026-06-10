---
id = "team-project"
version = "1.0.0"
tier = "any"
stacks = []
project_shapes = ["team-project"]
description = "Two or more people collaborate on a shared codebase with explicit coordination norms."
---

# Team Project

Multiple developers share ownership of a codebase. Work is coordinated explicitly:
branches, pull requests, reviews, and structured communication replace the informal
awareness a solo developer has.

## Project Structure Expectations

Standard repo layout with clear ownership boundaries. Add `CODEOWNERS` at repo
root to route reviews automatically. A `CONTRIBUTING.md` explains branch naming,
commit message format, and the PR process for new contributors.

Automated tooling (linters, formatters, type-checkers) is non-negotiable — teams
can't afford manual style enforcement. Formatting disputes are settled once in
a config file, never in review.

## Decision Norms

Significant architecture changes go through a lightweight RFC or ADR before
implementation. The goal is not bureaucracy — it's surfacing disagreements early
when changing direction is cheap.

Day-to-day decisions happen in PR comments. Reversible decisions don't need
a meeting; irreversible ones do.

## Code Review Conventions

Every PR requires at least one approver who wasn't the author. For changes to
shared utilities, interfaces, or schemas: two approvers, including one owner of
a dependent component.

Reviews focus on correctness first, then clarity. Style issues are handled by
the formatter, not by reviewers.

Block a PR only for correctness issues, not preferences. Use "Request Changes"
sparingly — a comment with "needs a fix" is usually enough.

## Release Cadence

Teams benefit from a predictable cadence: weekly or bi-weekly releases reduce
integration risk and give users expectations they can plan around. Hotfixes can
go out any time; they follow a fast-track review path (one approver, CI green).

Tag every release. Maintain a CHANGELOG. Deploy to staging before production.

## Documentation Expectations

`README.md` covers setup, development workflow, and testing. Architecture docs
live in `.github/docs/` or a linked wiki. New features include documentation
as part of the PR's acceptance criteria — docs are not optional follow-up.

On-call runbooks, deployment procedures, and incident response steps live
in a shared internal doc store accessible to all team members.

## Dependency Philosophy

Dependency additions require a brief note in the PR: what it does, why an
existing dep or hand-rolled solution won't work. This keeps the team informed
about what's in the production bundle and makes future audits tractable.

Keep dev dependencies pinned or audited via lockfiles. Security audits
(`pnpm audit`, `cargo audit`) run in CI.
