# Scoped chat reads

The daemon gives a launched agent (a principal) a token bound to one project
scope, such as `project:acme`. The chat verbs use that scope to decide which
conversation threads the agent may read or write. The owner CLI keeps its full
view.

## Who sees what

| Caller | Reads | Appends |
|---|---|---|
| No caller identity | Refused (`permission-denied`) | Refused |
| Owner CLI | Every thread, as before | Any thread |
| Principal for `project:a` | Threads attributed to `project:a` only | Threads attributed to `project:a`, or a new thread |

A principal never sees a thread attributed to another project, a thread the
owner created, or a thread written before attribution existed. It gets the same
`permission-denied` refusal for all of these and for a thread that does not
exist, so the refusal does not reveal which threads exist.

The rule covers `chat.list_threads`, `chat.get_thread`, `chat.search`,
`chat.topics_list`, `chat.threads_list` and `chat.append_turn`. All of them are
`owner-view` verbs in the access table (`internal/rpc/access_table_conversation.go`).

## Attribution

Each thread gets one row in `conversation_thread_scope` when it is created:

- A principal that calls `chat.append_turn` with no `thread_id` creates a
  thread attributed to its own scope.
- The owner CLI creating a thread (no `thread_id`, or a `thread_id` that does
  not exist yet) attributes it to `owner`.
- The row is written before the thread's first turn is stored and is never
  rewritten. A second write with another scope is refused as a conflict.
- Appending to an existing thread never adds a row. A thread from before this
  table existed stays unattributed, and only the owner can read or extend it.

A principal cannot create a thread by naming a new `thread_id`; it must let the
daemon mint one.

## The `scope` parameter

The five read verbs accept an optional `scope` string. It only narrows a view:

- A principal may send its own scope. Any other value is refused with
  `permission-denied`, and nothing is read.
- The owner may send any scope to see only the threads attributed to it, for
  example `owner` or `project:a`.
- An empty string is invalid input.

The parameter is never a source of authority. The scope always comes from the
caller's token.

## Search results for a principal

`chat.search` runs the query once per visible thread and merges the hits, so a
hit in another project can never take a visible hit's place under the limit.
For a narrowed view the `score` field is the hit's rank position (higher is better) rather than the raw full-text score. The raw score depends on term statistics across the whole index; reporting positions reduces what a principal can infer about other projects' threads. The order of a principal's own hits still comes from those index-wide statistics.

## Storage

The attribution table is its own versioned migration set,
`conversation-thread-scope`, applied the first time a scoped call needs it. It
has no foreign key to the thread table, for the same reason as the privacy-mode
table: the row must exist before the thread's first turn. Tests cover SQLite on
disk, SQLite in memory, and PostgreSQL under the `postgres` build tag.
