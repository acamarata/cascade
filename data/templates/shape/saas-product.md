---
id = "saas-product"
version = "1.0.0"
tier = "any"
stacks = []
project_shapes = ["saas-product"]
description = "A hosted product with paying users, continuous deployment, and operational concerns."
---

# SaaS Product

A live service with real users and uptime expectations. The codebase supports
continuous deployment to production. Operational concerns — observability, on-call,
data safety — are first-class alongside feature development.

## Project Structure Expectations

Typically a multi-repo or monorepo containing: frontend app, API service(s), shared
SDK or client library, and infrastructure-as-code. Each component has its own
deployment pipeline.

`infra/` or a dedicated infra repo manages cloud resources (Terraform, Pulumi, or
similar). Application secrets live in a secrets manager, never in the repo.

Environment configuration (dev / staging / prod) is explicit and versioned.
Staging matches production as closely as possible.

## Decision Norms

Features go through a product spec or issue brief before implementation starts.
Changes that affect the data model, billing, or auth require an architecture review.

All schema migrations are backward-compatible or run with a coordinated deploy.
Never break a running client with a server-side change without a migration window.

## Code Review Conventions

All PRs require review before merge to main. Production-impacting changes (auth,
payments, data migrations) require two reviewers.

Feature flags wrap incomplete or risky features so they can ship to production
but remain off until ready. This keeps `main` always deployable.

## Release Cadence

Deploy to production on every merge to main via CI/CD (continuous deployment) or
on a scheduled cadence (e.g. weekly). Hotfixes follow a fast-track path.

Maintain a status page and notify users of planned maintenance windows and incidents.
Tag releases for rollback capability.

## Documentation Expectations

Internal: architecture diagrams, runbooks, incident playbooks, on-call procedures.
External: product changelog, API reference (if public API), help center articles.

Every new feature that changes user-visible behavior includes a changelog entry
and, if needed, in-app guidance or documentation before it ships.

## Dependency Philosophy

Production dependencies are reviewed for security, license, and maintenance
health before adoption. Run automated security audits in CI (`pnpm audit`,
`cargo audit`). Subscribe to security advisories for critical deps.

Lock all production deps to exact or narrow version ranges to prevent surprise
breakage. Update deps on a regular schedule rather than reactively.
