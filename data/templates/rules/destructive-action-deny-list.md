---
id = "rule-destructive-action-deny-list"
version = "1.0.0"
tier = "any"
stacks = []
project_shapes = []
description = "Require explicit same-turn confirmation before any irreversible or high-impact action."
---

# Destructive Action Deny-List

Certain actions cannot be undone. Before executing any pattern below, stop,
state the blocker, and require the user to name the exact command or target
**in the same message** before proceeding.

## Actions Requiring Explicit Confirmation

**Filesystem**
- Recursive deletion of project roots, home directories, or mounted volumes.
- Overwriting a disk device (`dd`, `diskutil erase*`).

**Version control**
- Force-push or `--delete` on protected branches (`main`, `master`, `production`,
  or any branch named in the repo's branch-protection rules).
- Hard reset or `filter-branch` on published history.

**Database**
- `DROP TABLE`, `DROP DATABASE`, or `DROP SCHEMA` on any non-scratch database.
- `TRUNCATE` on production-prefixed tables.
- `DELETE FROM` without a `WHERE` clause.
- Restoring a backup over a live production database.

**Infrastructure and deployments**
- Deploying to a production environment (any CLI deploy command targeting prod).
- Deleting or shutting down a live server or cluster node.
- Destroying Terraform-managed resources.
- Dropping or resetting a production message queue, cache cluster, or object
  store bucket.

**Secrets and publishing**
- Committing or logging credential files (`.env`, `*.key`, `*.pem`, tokens).
- Publishing a package to a public registry (`npm publish`, `cargo publish`,
  `pip publish`, etc.) without an approved release plan.
- Bumping a version in any package manifest without an approved release plan.

**Outbound to humans**
- Sending email, messages, or social media posts to anyone other than the
  operator.

## When a Deny-List Pattern Is Hit

1. Refuse: "I cannot run `<pattern>` without explicit confirmation in this
   message."
2. Propose a safer alternative (dry-run, soft reset, dump before drop).
3. Never silently execute an adjacent version of the blocked command.
4. Offer this confirmation template:

```
AUTHORIZE: <exact command>
reason: <why this is needed>
consequence: <what cannot be undone>
```

Authorization applies to one execution, this turn only. It expires at end of turn.

## Safe Exemptions (Do Not Prompt)

- `rm -rf` on generated output directories (`node_modules/`, `dist/`, `build/`,
  `target/`, `.next/`, `.turbo/`, `.swc/`).
- `git branch -D feature/*` on feature branches.
- `git reset --hard HEAD` on the operator's own feature branch.
- Removing scratch Docker containers or volumes.
- Overwriting files via normal write tools (Edit, Write, atomic rename).
