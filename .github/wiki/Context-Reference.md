# Context Reference

## Session scope

`cascade context scope show [--json]` resolves the current session's
scope: a deny-by-default view of which project, product, and workspace
the current working directory belongs to, and which other scopes it may
see.

The resolved record carries exactly these fields:

| Field | Meaning |
|---|---|
| `kind` | `general` for an unresolved directory, or `session` once a repository is resolved. |
| `user` | Caller-supplied user identifier. |
| `machine` | Caller-supplied machine identifier. |
| `cwd` | The working directory scope was resolved from. |
| `workspace` | The resolved workspace id, if the project declares one. |
| `product` | The resolved product id, if the project declares one. |
| `project` | The resolved project id (the repository's own id). |
| `repository` | The repository record (remote URL + path hash), or absent. |
| `package_path` | The path from the repository root to `cwd`. |
| `branch` | Caller-supplied branch. |
| `task` | Caller-supplied active task id. |
| `session` | Caller-supplied session id. |
| `explicit_overrides` | Caller-supplied override value, preserved as-is. |

### Resolution order

1. Resolve the git root anchor for `cwd`.
2. Look up the repository record bound to that root.
3. If no repository is bound, return a **general** scope: user tiers plus
   the cascade-pa context only, with no project, product, workspace,
   branch, or task state. This is a successful, restricted result — never
   a fallback to an unscoped or global query.
4. Otherwise, walk the persisted scope graph outward from the project to
   its declared workspace/product membership.
5. Attach the caller-supplied branch, active task, and session.

### Explicit edges

Relationships between scopes are explicit, persisted data — never
inferred from name, path, or vocabulary overlap. Exactly three edge
values exist:

- `depends_on`
- `member_of`
- `shares_context_with`

An unknown edge value is refused with a typed invalid-input error, never
silently accepted or ignored.

### Deny-by-default candidate set

A session's candidate scope set is exactly:

- its own resolved scope chain (session → task → project →
  workspace/product), and
- the direct targets of `depends_on`/`shares_context_with` edges declared
  FROM a scope in that chain.

No other scope is visible. Two projects that happen to share vocabulary
never see each other's records unless an edge between them is explicitly
declared and persisted first.

## Traversal table

Visibility across the scope graph is governed by a single closed table,
`Traversal(kind, edgeClass)`, keyed on the scope kind (`session`, `task`,
`project`, `workspace`, `product`, `global`) and the edge class the
relationship maps onto:

- `member_of` → the `member` edge class
- `depends_on` and `shares_context_with` → the `route` edge class
- a session's own resolved chain → the `parent` edge class (never a
  persisted row)

A `(kind, edgeClass)` pair absent from the table is **not traversable** —
there is no permissive default. Widening the table requires updating the
pinned golden test alongside it, so an accidental widening fails CI
rather than silently shipping.

Cycles are rejected when a `member_of` edge is stored, not discovered
later when something tries to traverse it: an edge that would close a
containment cycle is refused with a typed invalid-input error at write
time.

### Addressing boundary

This scope graph and its traversal table are the routing authority a
notification or message address resolves against — they are not
themselves an authorization or capability system. Sending across a scope
boundary requires a separate capability check, carried by the components
that own delivery; the traversal table only decides what is visible, and
an address alone never widens that.

## Daemonless behavior

`cascade context scope show` works with or without a running daemon. When
a daemon is confirmed reachable, the command dials it over the daemon's
RPC surface (`context.scope.show`). Otherwise it resolves the scope
in-process against the local `cascade.db` file. Both paths return
identical data. The command never prompts, is unaffected by
`CASCADE_NO_INPUT`, and runs on Windows exactly like every other
one-shot, daemonless command.
