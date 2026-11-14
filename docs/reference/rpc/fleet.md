# Fleet RPC methods

Daemon methods in the `fleet.*` namespace that are documented here. Access
classes and elevation for every method are listed in the
generated RPC method reference. The wire envelope is described in
[the RPC protocol](../../rpc-protocol.md).

### `fleet.mode.show`

Reports the scheduler mode in force for one project.

Params (all optional): `project_id` and `lifecycle_stage`. When `project_id`
is empty the daemon asks its project resolver; if that cannot supply one the
call fails with `invalid-input` rather than falling back to a global mode.
When `lifecycle_stage` is given, the result also carries the default mode for
that stage; an unknown stage fails with `invalid-input`.

Result: `{"version": "1", "data": {...}}` where `data` holds `mode`,
`source`, `lifecycle_stage`, `lifecycle_default` and, when set, `set_at` and
`expires_at` (RFC 3339, UTC). A daemon whose scheduler-mode store is not
initialised answers `unavailable`.

### `fleet.sessions.completion_check`

Asks the completion gate whether a harness session may stop or mark a task
done. The hook pack command for the task-completed and stop events calls it.

Params (unknown fields are refused): `job_id`, `task_id`, `session_id`,
`event_type` and optional `stop_hook_active`.

Result: `{"deny": bool, "reason": string}`. The handler never returns an RPC
error for a bad request; it answers with `deny: true` instead. A malformed
payload, an unknown job id, an unconfigured gate, a gate error, a gate that
answers not-ok, and a check that exceeds its timeout (10 seconds by default)
all deny, each with a reason. A session that is not scoped to a job gets an
empty allow response. Each denial publishes a `jobs.gate.denied` event, or
`jobs.gate.timeout` for a timeout.

### `fleet.sessions.hook_event`

Ingests one harness hook event. Hook pack commands are the only intended
callers. The method is not registered on Windows, where no daemon runs.

Params (unknown fields are refused): `harness`, `event_type`, `session_id`,
`pid`, `account` and `timestamp_ms` (six fields; nothing else is accepted).
`harness` must be set and not `unknown`, `session_id` must be set, and
`timestamp_ms` must be positive and within 24 hours of the daemon clock;
otherwise the call fails with `invalid-input`.

Effect by event type: session and subagent start upsert an active session
record; prompt, instruction-load, tool and compaction events touch the matching
counter or timestamp on the record; stop moves the session to idle; session
end and subagent stop move it to closed; task and worktree events publish a
`jobs.*` event. An event type outside the known set is counted and dropped
without an error. On success the handler publishes
`fleet.sessions.hook_ingested` and returns an empty object.

### `fleet.sessions.list`

Lists session records, optionally filtered.

Params (unknown fields are refused): `harness`, `account`, `state`,
`parent_session_id`, each optional. Result: `{"sessions": [...]}`, each record
carrying `session_id`, `harness`, `account`, `pid`, `state`, `started_at`,
`updated_at`, the optional activity timestamps, `tool_count`,
`compaction_count`, the parent session and the untrusted flag. When the caller
is a session principal rather than the owner, the list is cut to that
session's own subtree.

### `fleet.mode.set`

Sets the scheduler mode for a project. The result has the same shape as
`fleet.mode.show`.

Params: `project_id` (when empty the daemon resolves the project, else
`invalid-input`), `mode` and optional `ttl`, a Go duration such as `30m`. An
unknown mode or a bad duration is `invalid-input`. With a `ttl`, the mode
expires after that time.

Setting `incident` loosens the scheduler, so it needs a verified elevation and
is written through the audit effect log. It needs a caller principal (else
`permission-denied`) and the audit writer (else `unavailable`, "incident audit
unavailable"). Other modes are set without those steps. When the mode
subsystem has not started, the call is `unavailable`.

Result: `{"version": "1", "data": {...}}` where `data` holds `mode`, `source`,
`set_at` and `expires_at` (both omitted when unset), `lifecycle_stage` and
`lifecycle_default`. After a set, the lifecycle fields are empty.

### `fleet.attention.list`

Lists items in the supervision attention queue.

Params (unknown fields refused): `own_scope` (required, else
`invalid-input`), `all`, `chain`, `kind` and `include_acked`. With `all` set,
the list covers every scope visible from `own_scope` through `chain`;
otherwise only `own_scope`. Acknowledged items are left out unless
`include_acked` is true. A session principal sees only items tied to its own
sources.

Result: `{"items": [...]}`.

### `fleet.attention.get`

Returns one attention item.

Params: `id` (required) and optionally `own_scope` and `chain`. When
`own_scope` is given, an item outside the visible scopes is `not-found`. A
session principal asking for an item outside its sources gets
`permission-denied`.

Result: `{"item": {...}}`.

### `fleet.attention.ack`

Acknowledges an attention item.

Params: `id` (required). A session principal may acknowledge only items tied
to its own sources, else `permission-denied`.

Result: `{"item": {...}}` with the acknowledged item.

### `fleet.health.summary`

Returns the fleet health at a glance.

Params: none; any other params are `invalid-input`.

Result: `status` (always `ok`) and `keys`, exactly five entries in the order
`executive`, `build`, `bulk`, `adversary`, `long_context`. Each has `key`,
`state` (one of `healthy`, `protected`, `scarce`, `abundant`, `available`,
`unavailable`) and, when known, `percentage`. A failing health source is
`unavailable`; a malformed one is `internal`.

### `fleet.journal_show`

Reads an entity's journal.

Params: `entity_id` (required), `after_seq` (entries after this sequence
number) and `limit` (at most 500; zero or absent returns all). An entity
with no journal is `not-found`; an `after_seq` past the journal head is
`invalid-input`.

Result: `{"entries": [...]}`.

### `fleet.journal_replay`

Replays an entity's journal in order.

Params: `entity_id` (required) and `from_seq` (entries after this sequence
number; absent starts from the beginning). Errors are as for
`fleet.journal_show`.

Result: `{"entries": [...]}`, with no limit.

### `fleet.lanes.list`

Lists the fleet lanes.

Params: optional `include_retired`.

Result: `{"version": "1", "data": [...]}`. Each lane has `id`,
`runtime_profile`, `quota_domain`, `model_id`, `effort`, `lane_class`,
`interaction_class`, `health` and `health_reason`, `base_shadow_price`,
`ceiling` and `ceiling_reason`, `retired` and `discovered_at`. The topology
methods share this envelope. A failed lookup keeps its error kind (or
`unavailable`) with a generic message. A response the credential detector
flags is withheld as `integrity`.

### `fleet.lanes.show`

Returns one lane.

Params: `id` (required, else `invalid-input`).

Result: the envelope with `data` holding the lane.

### `fleet.lanes.explain`

Returns a lane with the runtime profile and quota domain behind it.

Params: `id` (required).

Result: the envelope with `data` holding `lane`, `profile` (`id`, `runtime`,
`persistent`, `local`) and `domain`.

### `fleet.domains.list`

Lists the quota domains.

Params: none.

Result: the envelope with `data` a list of domains, each with `id`, `kind`,
`billing_tier`, `quarantined`, `holders` (the credential count) and `buckets`.
A bucket has `name`, `window`, `source`, `limit`, `remaining_fraction`,
`confidence`, `reset_at` and `observed_at`.

### `fleet.domains.show`

Returns one quota domain.

Params: `id` (required).

Result: the envelope with `data` holding the domain.

### `fleet.domains.promote`

Promotes one quota domain to the `paid` billing tier after probing and keying
every provider that holds a credential in it.

Params: `tier` (required, must be `paid`) and exactly one of `domain` or
`provider` (a provider resolves to the one domain its credentials reference).

Result: `{"domain", "tier", "providers", "evidence"}`, where `providers` is the
sorted provider list and `evidence` the number of evidence rows written. A
failed probe or key writes nothing.

Refusals: owner-only; always requires local elevation (`elevation-required`
without it). `invalid-input` for a wrong tier or when neither or both of
`domain` and `provider` are named; `not-found` for an unknown domain or a
provider with no domain; `conflict` for a provider spanning several domains.

### `fleet.domains.demote`

Sets one quota domain back to the `discover` billing tier and prunes stale
billing evidence.

Params: `tier` (optional, empty or `discover`) and exactly one of `domain` or
`provider`.

Result: `{"domain", "tier", "pruned", "prune_error"}`. `pruned` counts the
evidence rows dropped; a prune failure keeps every row and is returned as
`prune_error`.

Refusals: owner-only; the same `invalid-input`, `not-found` and `conflict`
cases as `fleet.domains.promote`.

### `fleet.domains.forget`

Deletes one provider's billing evidence rows.

Params: `provider` (the provider to forget) and `tier` (optional, empty only
here; any other value is `invalid-input`).

Result: `{"provider", "forgotten"}`, where `forgotten` is the number of rows
deleted.

Refusals: owner-only.
