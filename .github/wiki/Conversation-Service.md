# Conversation Service

`internal/conversation` is the append-only conversation store (threads,
turns, segments) plus the read/lifecycle capabilities layered on top of
it: cursor pagination, full-text search, and windowed retention with
thread archival.

## Pagination

`ListTurnsPage(ctx, threadID, PaginationFilter)` and `ListThreadsPage(ctx,
PaginationFilter)` return a bounded page plus an opaque `NextCursor`.

- `PaginationFilter.Cursor` is `""` for the first page. A non-empty
  cursor must be one this package returned from a prior call -- it is
  never constructed by a caller. Passing a turn cursor issued for one
  thread to a `ListTurnsPage` call for a different thread is refused
  (`ErrBadCursor`), not silently accepted at the wrong offset.
- `PaginationFilter.Limit` is clamped to a bounded range (a caller cannot
  request an unbounded read); `<= 0` uses the domain default.
- `NextCursor == ""` means the page returned is the last one.
- A malformed, truncated, or foreign cursor returns `ErrBadCursor` and
  never panics -- enforced by `FuzzCursorDecode` over adversarial bytes.

## Search

`SearchTurns(ctx, query, SearchFilter)` runs a parameterised SQLite FTS5
`MATCH` query over every appended segment's content, populated on append
(no background re-index step), and returns `TurnMatch` records ranked by
SQLite's own `bm25()` relevance (lower is more relevant).

- The `MATCH` query is always bound as a driver parameter -- a caller's
  search text can produce a malformed-FTS5-syntax error but can never
  reach raw SQL, so there is no injection surface regardless of content.
- `SearchFilter.ThreadID` optionally scopes a search to one thread.
- `ErrSearchUnavailable` is returned, never a panic, when the underlying
  store has no FTS5 index -- the case for a non-SQLite storage profile
  (FTS5 has no portable Postgres equivalent).
- A thread's archived state (below) does not affect search: an archived
  thread's turns remain fully searchable.

## Retention

`PruneTurns(ctx, RetentionPolicy, now)` performs a **logical** prune: it
tombstones (never deletes) turns that are simultaneously older than
`AgeMaxDays` and beyond the `TurnsMaxPerThread` most-recent turns in
their own thread. Physical space reclaim remains a separate VACUUM
job's responsibility.

- Either policy field `<= 0` disables pruning entirely -- a zero-window
  policy is a documented no-op, not an error.
- Idempotent: a repeat call against unchanged data tombstones zero
  additional rows (`PruneResult.Tombstoned == 0`).
- Tombstoning does not remove a turn from normal listings or search -- it
  only marks it as a future VACUUM candidate.

## Archival

A thread carries an archived/not-archived state (R-14.90), tracked in a
dedicated marker table rather than a column on the thread row itself, so
no turn or segment data is ever touched by archiving.

- `ArchiveThread(ctx, threadID)` / `UnarchiveThread(ctx, threadID)` flip
  the state; unarchiving a thread that was never archived is a no-op.
- An archived thread is excluded from `ListThreadsPage`'s default
  listing; pass `PaginationFilter.IncludeArchived: true` to see it
  there too.
- An archived thread's turns are never deleted, modified, or excluded
  from `SearchTurns` -- archival hides a thread from casual listing, it
  does not remove its history.

## Adapter surface

`Adapter` (the JSON-RPC layer over `Store`) exposes each of the above as
a plain Go method -- `ListTurnsPage`, `ListThreadsPage`, `SearchTurns`,
`ArchiveThread`, `UnarchiveThread`, `PruneTurns` -- for a future
CLI/MCP surface to call. None of them are registered on the JSON-RPC
registry yet: `chat.append_turn`, `chat.get_thread`,
`chat.list_threads` and `chat.search` are the wire methods today.

## Thread privacy modes

Every thread carries a privacy mode: one of the four §5.16 sensitivity
tiers (`local-only`, `restricted`, `internal`, `public`).

It lives in its own marker table, `conversation_thread_privacy`, rather
than as a column on the thread row -- the typed migration DSL has no
ALTER step, so adding a column to an existing table means authoring a new
table, the same shape `conversation_thread_archive` already uses.

**Absence means restricted.** A thread with no row in that table reads
back `restricted`, and so does a thread that does not exist and a row
whose stored text is not a tier name. The fail-closed default is
therefore true by construction rather than by a branch someone could
invert. `provider.SensitivityTier`'s own zero value is `restricted` for
the same reason.

`chat.append_turn` takes an optional `privacy_mode`, applied to the
thread that call CREATES. Sent with an existing `thread_id` it is
REFUSED, naming the thread: a caller that believes it privatised a thread
and did not is the failure this prevents. The mode is written before the
turn is committed, so there is no window in which a thread has content
and reads as looser than it was created.

`Adapter.SetThreadPrivacy` / `Adapter.ThreadPrivacy` are the Go surface.
Enforcement is the conductor's -- see the security posture note on
[thread privacy modes](https://github.com/acamarata/cascade/blob/main/docs/security-posture/egress-firewall.md).
