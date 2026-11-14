# Chat RPC methods

Methods for conversation threads and turns. Access classes and elevation are
listed in the generated RPC method reference. A thread stored under
the local-only privacy tier is served only to the owner CLI; other callers get
`permission-denied`.

Several of these methods accept an optional `scope` param that narrows the
call to the threads of one declared scope. When it is given it must be a
non-empty string, otherwise the call fails with `invalid-input`.

### `chat.append_turn`

Appends a turn to a thread, creating the thread when `thread_id` is empty.

Params: `thread_id`, `role`, `segments` (a list of `{kind, content}`),
and optionally `privacy_mode` (only on the call that creates a thread; sending
it on a continuation is refused), `origin`, `untrusted`, `leading_context`
(stored as segment 0 of this turn) and `cwd` (read only when the thread is
created, to record its project). An unknown role or a malformed payload is
`invalid-input`.

Result: `thread_id`, `turn_id`, `seq` (the turn's position, from 0) and
`topic_warning` (present only when the topic engine reported a problem; the
turn is stored either way).

### `chat.get_thread`

Returns one thread with every turn and its segments.

Params: `thread_id` (required, no other fields allowed).

Result: `thread` and `turns`, a list of `{turn, segments}`. Each turn carries
an `untrusted` marker. An unknown thread is `not-found`. A failed privacy read
is `unavailable`.

### `chat.list_threads`

Lists the threads the caller may see.

Params: none, apart from the optional `scope`.

Result: `threads`, a list of thread records. Threads the caller may not list
are left out.

### `chat.search`

Full-text search over turn content.

Params: `query` (required, non-empty), `thread_id` (optional, limits the
search to one thread) and `limit`.

Result: `results`, a list of `{thread_id, turn_id, content, score}`, with
`origin` and `untrusted` when the matching turn has them. `content` is the
whole matching turn; the caller cuts any excerpt.

### `chat.threads_list`

Lists threads as topic summaries, one page at a time.

Params: `page` (from 1; lower values become 1), `page_size` (default 20,
at most 100) and optional `scope`.

Result: `threads`, a list of `{topic_type, id, turn_count, last_activity}`
(`last_activity` is Unix seconds, 0 for an empty thread), plus `page`,
`page_size` and `total_count`.

### `chat.topics_list`

Lists topic summaries.

Params: optional `scope` only.

Result: `topics`, a list of `{topic_type, thread_count, turn_count}`.
