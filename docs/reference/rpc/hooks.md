# Hook RPC methods

Daemon methods that expose the hook dispatcher. Access classes are listed in
the generated RPC method reference. Both methods take no
parameters: a missing or `null` body is accepted, any field or trailing data
fails with `invalid-input`. Both answer `unavailable` ("hooks subsystem
degraded") when the daemon runs without a hook dispatcher.

### `hooks.list`

Lists the configured hooks and the dispatcher's counters.

Result: `{"hooks": [...], "stats": {...}}`. Each hook entry carries `id`,
`namespace`, `trigger`, `action_type`, `param_keys` (sorted) and
`params_hash`, a SHA-256 over the action parameters. The parameter values
themselves are never returned. `stats` holds `registered`, `last_fire` (RFC
3339 UTC, or `null` before the first fire) and `budget_refusals`.

### `hooks.fires`

Returns the newest recorded fires (at most 100, oldest first), as `{"fires": [...]}`; the
list is empty, never `null`, when nothing has fired. Each fire carries
`hook_id`, `namespace`, `trigger`, `action_type`, `event_seq`, `depth`,
`params_hash`, `result_code`, an optional `err_msg` and the timestamp `ts`.
