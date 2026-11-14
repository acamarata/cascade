# Supervisor RPC methods

Methods that expose fleet supervision state. Access classes are listed in the
generated RPC method reference. The supervisor events these
methods describe travel on the event stream covered in
[the RPC protocol](../../rpc-protocol.md#supervisor-methods-and-events).

### `supervisor.snapshot`

No params; an empty body is accepted, any field is `invalid-input`.

Result: `schema_version` (currently 1), `attention_queue_depth`,
`stall_count`, `sessions` (one entry per known session, each with
`session_id`, `action_count` and `interrupt_count`), `headroom_ceiling`
(omitted when there is none), `autonomy_profile` and `auto_advance_tier`.

A daemon whose supervision surface is not running answers `unavailable`. A
request whose context is already canceled answers `canceled`.

### `supervisor.events_schema`

No params. Returns `{"schema_version": 1, "events": [...]}`, one entry per
supervisor event kind, each with its `kind` and a `fields` list of
`{name, type}` pairs. The four kinds and their fields, after the shared
`schema_version` (int):

| Kind | Fields |
|---|---|
| `supervisor.attention_added` | `item_id`, `kind`, `scope_kind`, `scope_id` (strings) |
| `supervisor.stall_detected` | `session_id`, `stall_kind` (strings) |
| `supervisor.escalation` | `session_id`, `rung` (strings) |
| `supervisor.headroom_update` | `resource` (string), `enforced_ceiling` (int64), `ratio` (float64) |
