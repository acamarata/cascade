---
id = "standard-git"
version = "1.0.0"
tier = "any"
stacks = []
project_shapes = []
description = "Universal Git standard: conventional commits, small PRs, no force-push to protected branches, no secrets committed."
---

# Git Standard

## Commit Messages

- Use Conventional Commits format: `<type>(<scope>): <subject>`.
  - Types: `feat`, `fix`, `chore`, `docs`, `refactor`, `test`, `ci`, `perf`, `build`.
  - Subject: imperative mood, ≤72 characters, no trailing period.
  - Body (optional): explain *why*, not *what*; wrap at 72 characters.
- One logical change per commit. Avoid "WIP" or "fix stuff" messages on main.
- Reference issues in the footer: `Closes #42` or `Refs #42`.

## Branch Workflow

- `main` (or `master`) is always deployable. Never commit directly to it.
- Feature branches: `feat/<slug>`, bug fixes: `fix/<slug>`, releases: `release/<version>`.
- Rebase feature branches onto main before opening a PR to keep history linear.
- Delete branches after merge.

## Pull Requests

- Keep PRs small: ≤400 lines changed is a strong target; ≤200 is ideal.
  Large changes should be split into sequential PRs or feature-flagged.
- Every PR has a description explaining *why* the change is needed.
- All CI checks must be green before merge.
- Squash-merge feature branches to keep the main history clean. Use merge commits
  only for release branches where individual commits matter.

## Protected Branches

- Never force-push to `main`, `master`, or any branch marked protected.
- Never use `--force-with-lease` on protected branches without team agreement.
- `git reset --hard` on local feature branches is fine; on shared branches, create
  a revert commit instead.

## What Never Goes In Git

- Secrets, credentials, tokens, or private keys of any kind.
- `.env` files containing real values (template/example `.env.example` is fine).
- Build artifacts (`dist/`, `target/`, `__pycache__/`, `node_modules/`).
- OS noise (`.DS_Store`, `Thumbs.db`).
- Editor-specific files unless the whole team agrees to track them.

Add these patterns to `.gitignore` *before* writing the files. A secret
committed even once is compromised — rotate it immediately if it happens.
