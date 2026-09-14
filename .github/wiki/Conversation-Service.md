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
registry yet: only `chat.append_turn`, `chat.get_thread`, and
`chat.list_threads` are wire methods today.
