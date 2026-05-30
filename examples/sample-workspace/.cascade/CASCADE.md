# PPC — sample-workspace Project Rules

**Tier:** PPC (Per-Project Cascade)
**Project:** sample-workspace
**Inherits from:** GCI → APC
**Repos in this project:** sample-workspace (single-repo project)

---

## Project Overview

This is a single-repo project. The PPC tier exists for projects with multiple repos;
here it is minimal and mostly passes through to the repo-level PRC.

For multi-repo projects, this file is where cross-repo concerns live:
- Shared API contracts between repos
- Monorepo tooling decisions
- Cross-repo deployment sequencing
- Shared credentials and environment variable conventions

---

## Coding Language

TypeScript strict mode throughout. No `any` without a justification comment explaining why.

## Package Manager

pnpm is the only allowed package manager. No npm install, no yarn, no bun.

## Environment Variables

All environment variables are documented in `.cascade/docs/ENV.md`. New variables
are added there before they are referenced in code.

---

## Cross-Repo Contracts

None — single-repo project. This section is filled in for multi-repo projects.
