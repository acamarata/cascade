# Policy RPC methods

Methods over the policy engine and its audit log. Access classes and
elevation are listed in the generated RPC method reference. For
the policy model see [policy model](../../policy-model.md). The
`policy.explain`, `policy.check`, `policy.list` and `policy.audit_query`
methods first run the policy authorization check for the caller; params are
decoded leniently (an empty or `null` body means defaults, an unreadable
body is `invalid-input`).

### `policy.explain`

Evaluates one action and explains the verdict.

Params: `subject` (`{"kind", "id"}`), `capability` and `action`. `action` is
required: an empty action is `invalid-input`. A daemon with no policy engine
fails the call.

Result: `verdict`, `level`, `layer`, `reason`, `matched_rule` and
`explanation`.

### `policy.check`

The same evaluation as `policy.explain` with the same params, returning only
the outcome: `verdict`, `level` and `auto_advance`.

### `policy.list`

No params. Returns `capabilities` (the capability registry, an empty list when
none is wired) and `verbs`, one entry per registered verb with `method`,
`risk`, `elevated` and, when the verb has one, `mcp_tool`.

### `policy.audit_query`

Reads the audit log.

Params: `filter`, a list of filter expressions in the audit filter syntax.
A malformed filter is `invalid-input`; a daemon with no audit log answers
`unavailable`. Result: `records` (always a list) and, when more remain,
`next_cursor`.

### `policy.authorize`

Opens a same-turn window that lets one exact action through the deny list
once. Elevated: the call needs a verified elevation bound to its params, else
it fails with `elevation-required`.

Params: `action`, the exact action text. The window belongs to the calling
subject: the MCP subject, scoped to the calling principal when there is one.
Starting a new authorization ends the previous window. An action the policy
refuses unconditionally, an elevated RPC verb, a duplicate of an open window
and an invalid action are all refused. Every grant and every refusal is
written to the audit log; if the grant cannot be audited it is withdrawn and
the call fails with `unavailable`.

Result: `{"nonce": "...", "expires_at": "..."}`. The window closes after two
minutes. A daemon without a policy engine answers `unavailable`.
