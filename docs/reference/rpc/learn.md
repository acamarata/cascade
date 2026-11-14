# learn RPC methods

Daemon methods that expose the capability scorer's learned observations. All
three are owner-only; a caller principal never reads or resets them.

### `learn.status`

Lists the capability score for every stored scope, task class and tier.

Params: none (a missing body is accepted).

Result: `{"scores": [...]}`, sorted by tier then task class. Each row carries
`tier`, `task_class`, `score`, `alpha`, `beta`, `observations` and
`fallback_level` (`priors` when there are no observations). With no stored
rows it lists the global priors.

### `learn.explain`

Explains the scores of one scope and task class across every tier, and
whether the expected-time jump fires.

Params: `scope_key` (required; `global`, `repo:<opaque id>` or
`lang:<language>`) and `task_class` (required; must have a capability prior).

Result: `{"scores", "tier0", "best_lower", "threshold", "jump_fires"}`.
`scores` holds one row per tier (same fields as `learn.status`). `tier0` and
`best_lower` are estimates with `tier`, `queue_wait`, `duration_est`,
`p_rework`, `rework_cycles_est` and `expected_time`.

Refusals: `invalid-input` for a malformed `scope_key` or a task class with no
capability prior.

### `learn.reset`

Deletes learned observations, by scope or all of them.

Params: `scope_key` (required unless `all` is true), `all` (optional boolean),
`task_class` (optional filter) and `tier` (optional filter).

Result: `{"rows_deleted", "changed"}`, where `changed` is true when at least
one row was deleted. Repeating a reset is safe.

Refusals: `invalid-input` when neither `scope_key` nor `all` is given, or for
a malformed `scope_key`, an invalid `tier` or a task class with no capability
prior.
