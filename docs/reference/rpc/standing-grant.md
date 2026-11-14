# Standing grant RPC methods

Methods over standing grants: long-lived allowances for one grantee and one
capability. Access classes and elevation are listed in the
generated RPC method reference. For the policy model see
[policy model](../../policy-model.md). Each call first runs the policy
authorization check for the caller. Params are decoded leniently (an empty or
`null` body means defaults, an unreadable body is `invalid-input`).

Methods that touch the grant store answer `unavailable` when the daemon has
none.

### `standing_grant.list`

Params: `subject` (`{"kind", "id"}`). Result: `{"grants": [...]}`, always a
list, the grants held for that subject.

### `standing_grant.create`

Creates a standing grant. Elevated: it always needs an elevation grant.

Params: `grant_id` (a well-formed grant id), `action_class`, `action`,
`capability`, `scope`, `grantee` (`{"kind", "id"}`) and `exp` (an RFC 3339
expiry). A malformed `grant_id`, or an action class that may not be granted
standing, is `permission-denied`.

Result: `{"grant_id", "action", "changed": true}`.

### `standing_grant.change`

Elevated, like create. The handler takes the same params and runs the same
path as `standing_grant.create`: it stores the grant described by the params
and returns the same result. It does not read or amend an existing grant by
itself.

### `standing_grant.revoke`

Params: `grantee` (`{"kind", "id"}`) and `capability`. Revokes the grantee's
grant for that capability and returns `{"action": "<capability>", "changed":
true}`.
