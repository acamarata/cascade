# Inbox and notification RPC methods

Methods over the notification inbox and the notification service. Access
classes are listed in the generated RPC method reference. For the
notification model itself see [notifications](../../notify.md).

Inbox params must be one JSON object (an empty body counts as `{}`); unknown
fields fail with `invalid-input`. A notification id must be non-empty, at most
256 bytes and free of NUL, CR, LF, tab and space.

Visibility is decided per caller. An owner sees everything with `all: true`.
A session principal sees addressed items meant for it, global critical items,
and scoped or shared items whose target scope is its own. A caller that is
not the owner CLI is also held to the model data-class ceiling: items above it
are hidden and counted in `withheld` instead.

### `inbox.list`

Params: `all` (owner only; a non-owner asking for it gets `permission-denied`)
and `unread` (skip items the caller has already acknowledged).

Result: `{"items": [...], "total", "unread", "withheld"}`. Items are envelopes
carrying `id`, `class`, `origin_scope`, `data_class`, `trust` and a `summary`,
sorted by id. Expired notifications are skipped.

### `inbox.read`

Params: `id` and `offset` (default 0, never negative).

Result: one page of the notification body, escaped for control characters:
`envelope`, `heading`, `body`, `offset`, `next_offset`, `total_bytes` and
`done`. A page is at most 12 KiB, cut on a UTF-8 boundary and halved further
if the encoded reply would exceed the envelope cap. Read again with
`next_offset` until `done` is true. An unknown or invisible id answers
`not-found`, as does an expired one; an offset past the end is
`invalid-input`.

### `inbox.ack`

Params: `id`. Marks the item acknowledged for this caller only and returns
`{"id", "acked": true}`. Acknowledging twice fails with `conflict`; an unknown,
invisible or expired id is `not-found`.

### `notify.stats`

No params. Owner only: any session principal gets `permission-denied`.
Result: `{"dropped": {reason: count}, "accumulated": n}`, the per-reason drop
counters and the number of notifications currently held in the accumulation
buffer.
