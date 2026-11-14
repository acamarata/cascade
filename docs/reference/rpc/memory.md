# Memory RPC methods

Methods over the memory store, its review queue and the soul document. Access
classes and elevation for every method are listed in the
generated RPC method reference. For the memory model see
[memory architecture](../../memory-architecture.md).

Memory records are addressed as `<kind>/<name>`, where kind is one of `user`,
`feedback`, `project` or `reference`. Params are decoded leniently: an empty or
`null` body means all defaults, a malformed body is `invalid-input`.

### `memory.remember`

Writes one memory record.

Params: `content` (required; blank content is `invalid-input`), and optional
`type` (a kind; default `project`), `name` and `provenance` (a session id).
When `name` is omitted it is the first 16 characters of the content hash. The
name must be a valid record name.

Result: `{"id": "<kind>/<name>"}`. The record is stored with scope `local`
and full confidence. A session principal writes into its own scope and is
refused when the record already exists in a scope it cannot see.

### `memory.recall`

Searches records by case-insensitive substring over name, description and body.

Params: `query`, `k` (the maximum number of results; `k` of zero or less
returns an empty list), and optional `type` and `scope`. Result:
`{"units": [...], "unreadable": [...]}`; `unreadable` lists records that could
not be read, each with an `id` and a `reason`. Results come in address order.
A session principal sees only records visible to its scope and may not
declare a scope it does not own; the owner may pass `scope` to filter.

### `memory.list`

Pages through records.

Params: optional `type`, `limit` (default 100), `cursor` and `scope`. Result:
`{"units": [...], "next_cursor": "...", "unreadable": [...]}`. Pass
`next_cursor` back as `cursor` to continue; it is absent on the last page.
Scope rules match `memory.recall`.

### `memory.forget`

Deletes one record.

Params: `id` (a `<kind>/<name>` address), optional `reason` and `dry_run`. A
dry run only checks the record exists (`not-found` if it does not) and returns
`{"id", "dry_run": true, "forgotten": false, "index": {...}}`.

A real forget runs the forget pipeline when the daemon has one wired, and
returns what it reports: `forgotten`, `already_forgotten`, a list of `traces`
(each with `place`, `disposition` and `detail`; dispositions are `removed`,
`retained`, `unreachable` and `not_configured`), the `index` trace and
whether a forgotten event was emitted. Without a pipeline the record file is
unlinked after its tombstone is written and the traces say plainly that no
index, vector or backup note was touched. A session principal may only forget
records visible to its scope; anything else is refused.

### `memory.consolidate`

Runs memory consolidation: merges near-duplicate records and retires the
merged-away ones.

Params: optional `dry_run`. The configured consolidation settings apply, with
`dry_run` taken from the request. Result: the consolidation report, with
`merged`, `retired`, `no_change`, `skipped`, `method`, `dry_run` and, when
there are any, `groups`.

### `memory.review.list`

Lists the review queue of memory candidates.

Params: optional `section`, `pending` or `promoted`; empty means both. Result:
`at` (the instant the queue was read), the thresholds `min_ref_count` and
`min_sessions`, and the `pending` and `promoted` candidate summaries. A
summary carries `id`, `kind`, `ref_count`, `sessions` and `status`.

### `memory.review.act`

Acts on one candidate.

Params: `id` (a `<kind>/<name>` address), `action` and optional `defer_days`.
Actions: `approve` promotes the candidate, `skip` leaves it unchanged,
`defer` snoozes it for `defer_days` (7 when zero; negative or over
365 is `invalid-input`), `revert` reverts it. Any other action is
`invalid-input`. Result: `action`, `changed`, the candidate `item` and `at`.
Every action is reported to the review event sink.

### `memory.soul.show`

No params. Returns the soul document: `body`, `schema`, `version` and
`diverged`.

### `memory.soul.edit`

Params: `body` and optional `schema`. Replaces the soul document and returns
`{"version": n}`, the new version.

### `memory.soul.export`

No params. Returns the export envelope: `schema_version`, `exported_at` (UTC),
the current `soul` document and the full `audit_entries` change log in version
order.
