# Approval RPC methods

Methods over the approval queue. Access classes and elevation are listed in
the generated RPC method reference. For the model see
[policy model](../../policy-model.md). Each call first runs the policy
authorization check for the caller. Params are decoded leniently (an empty or
`null` body means defaults, an unreadable body is `invalid-input`). Methods
that need the queue answer `unavailable` when none is wired.

### `approval.list`

No params. Result: `{"pending": [...]}`, always a list. Each entry has
`request_id`, `action_summary`, `exp` (when the approval stops being
redeemable) and `action_class`.

### `approval.show`

Params: `request_id` (required). Returns one entry. When the daemon has an
approval entry source, the entry also carries `state` and may be a request
that is no longer pending; without one, only pending requests are found. An
unknown request is `not-found`.

### `approval.deny`

Params: `request_id` (required), `presented_summary` and `presented_level`,
the summary and risk level the operator was shown. Records a denial through
the queue and returns `{"request_id", "state"}`. A refusal by the queue is
returned as the call's error.

### `approval.grant`

Approves a request in one of two ways, chosen by whether `signed_token` is
present. `request_id` is required in both.

- Without a token, the call approves a held, pending request. It needs a local
  attestation for this call and the held signed record; the record must verify
  and match the request, and the audit history must verify. Result:
  `{"request_id", "state": "approved"}`.
- With a base64 `signed_token`, plus `action` and `params`, the call redeems
  the approval against that exact action. Result: `request_id`, `capability`,
  `level` and `consumed_at`.

Either path is recorded as an audited effect, so a request cannot be granted
twice. Every refusal is `permission-denied`, with a reason such as an expired
or unverifiable token, a token issued for another request, a mismatch with the
action, a failed audit check, or a process where the verifier, queue, held
record source, effect writer, audit verifier or recorder is not wired. Each
refusal is also written to the audit log.

### `approval.expire`

No params. Expires pending approvals whose time has passed and returns
`{"expired": n}`.
