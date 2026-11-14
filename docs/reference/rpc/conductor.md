# Conductor RPC methods

Methods for running model work and delegating tasks. Access classes and
elevation are listed in the generated RPC method reference.

### `conductor.execute`

Runs one model request through the router and the executor, applying the
conductor's classification, policy, sensitivity and egress checks. When the
executor could not be built at start-up, every call fails with `unavailable`.

Params: `task_id`, `task_class`, `inputs` (a list of `{role, content}`),
`requirements`, `sensitivity` (a tier name; an unknown name is
`invalid-input`) and optionally `fan_out` and `request_id`. A `fan_out`
above 1 runs that many legs; a value over the conductor maximum, or a
`request_id` that is not a valid id, is `invalid-input`.

Result: `job_id`, `selection` (the router's pick), `output` (the final text),
`usage` and, for a fan-out call, `legs`, one response per leg. A fan-out
parent has an empty `output`, and its `usage` is the sum of the legs.

### `conductor.expand`

Returns the stored evidence packet behind a claim, one page at a time.

Params: `claim_id` (required) and `page_token` (empty for the first page).
A malformed or unknown token is `invalid-input`; an unknown claim is
`not-found`.

Result: `claim`, `evidence` (the claim's evidence rows), `content` (the
current page) and `next_page_token`, empty on the last page. A page holds at
most one evidence item, cut at 16384 bytes, and a cut item is marked
truncated.

### `conductor.delegate`

Starts a delegated agent run and returns without waiting for it.

Params: `intent` (required, at most 64 KiB), and optionally `task_class`,
`min_quality`, `deadline`, `mutation`, `job_id` and `wait_timeout_ms`
(must not be negative). Unknown fields are refused. The call checks the
delegation need, then spawns the agent under the caller's delegation.

Result: `job_id` and `status`, always `dispatched`.

Errors: `invalid-input` for bad params. A request with `mutation` true is
refused with `unavailable`, because this daemon does not yet resolve the
worktree binding that mutation delegations need. A caller with no delegation
to delegate from gets `permission-denied`. When the delegation service is not
wired the call is `unavailable`.

### `conductor.delegate_wait`

Waits for the result of a delegation. Only the delegation's origin may wait.

Params: `job_id` (required) and `timeout_ms` (not negative; capped at ten
minutes).

Result: `job_id`, `state`, `output` and `artifacts` (both omitted when
empty) and `data_class`. When the timeout passes first, the stored state is
returned.
