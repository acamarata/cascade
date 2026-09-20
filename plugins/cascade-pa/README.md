# cascade-pa

A builtin plugin. It owns `cascade chat` and the three MCP tools a harness
session uses to reach the operator's conversation store.

## The MCP tools a session sees

Registered by this plugin's manifest (`Provides.Tools`) and served by its
`DispatchTool`. A harness sees them only when the plugin is enabled — the
MCP surface is generated from the manifest, so a tool missing from that list
is absent from every harness no matter what the dispatcher could service.

| Tool | What it does |
|---|---|
| `cascade_cpa_send` | Records one turn, starting a thread when none is named. |
| `cascade_cpa_history` | Reads a thread's turns, or lists the threads when none is named. |
| `cascade_cpa_search` | Finds turns whose content matches a query. |

They answer the same three questions `cascade chat` asks, through the same
daemon methods, so an agent and a person see one conversation rather than
two.

### `cascade_cpa_send`

```jsonc
// in
{"content": "…", "thread_id": "…", "sensitivity": "local-only|restricted|internal|public"}
// out
{"turn_id": "…", "thread_id": "…", "created_at": "RFC3339", "sensitivity": "…"}
```

`content` is required. Omit `thread_id` to start a thread; the result names
the one the **server** minted, never the empty string the caller sent.

`sensitivity` fails closed (06 §5.16): unset, unknown, misspelled and
differently-cased all resolve to `restricted`. `"Public"` is not `public`.
The resolved tier is echoed in the result, so a caller that misspelled one
is told its request was narrowed rather than believing its public turn is
public.

**What the tier does not yet do.** It is resolved and reported; it is not
stored. `chat.append_turn` has no sensitivity field and `internal/conversation`
has no sensitivity concept, so nothing downstream reads the tier back. Recorded
as a gap against the conversation domain, not papered over here.

### `cascade_cpa_history`

```jsonc
// in
{"thread_id": "…", "limit": 20, "before_turn_id": "…"}
// out — turns when thread_id is given, thread summaries when it is not
{"items": [ … ], "next_cursor": "…"}
```

`limit` defaults to 20 and is **clamped** to 100 rather than refused: an
agent asking for a thousand turns wants as much as it can have. A zero or
negative limit is the default, because zero is what an omitted field decodes
to.

Paging runs backwards from the recent end of the thread — that is what an
empty cursor means — and each page reads oldest-first, because that is the
order a conversation reads in. An empty `next_cursor` means the start has
been reached. A cursor naming a turn the thread no longer contains returns
the newest page rather than an error, so a caller holding a stale cursor is
not stranded.

Without `thread_id` the result is the thread listing: `{id, title, updated_at}`.
`updated_at` is **derived** from the newest turn, because `chat.list_threads`
carries no modification time; it costs one extra read per thread. A thread's
only mutation is an appended turn, so that derivation is exact.

### `cascade_cpa_search`

```jsonc
// in
{"query": "…", "thread_id": "…", "limit": 10}
// out
{"results": [{"thread_id": "…", "turn_id": "…", "excerpt": "…", "score": 1.0}]}
```

A case-insensitive substring **scan**, most recent turn first, at this point
in the plan. `score` is 1.0 on every match by construction: a substring scan
has no relevance to report, and a fabricated gradient would be a ranking
nobody computed. The FTS5-backed search replaces the scan when S-44.T3 lands
and preserves this shape — which is why the field exists now.

An empty or whitespace-only query is refused. Every turn contains the empty
string, so answering would look like a working search that found the whole
store. A thread that cannot be read is skipped rather than failing the
search: one unreadable thread should not make the other nine unsearchable.

## Wiring

The tools reach the daemon through `tools.Conversations`, injected by
`internal/plugins/cascadepa_tools_wiring.go`. This package may not import
`internal/**` (Art.10.2, R-14.69), so the implementation lives at the
composition root and is handed in at startup.

Before a service is injected every tool **refuses**, naming what is missing.
It does not answer from nothing: "this process cannot reach the conversation
store" and "you have no conversations" are different facts, and an agent
handed the second when the first is true reports lost history to its
operator.

## Evidence

`plugins/cascade-pa/testdata/mcp_fixtures/` holds a complete recorded session
from a real MCP client — the client launched the server itself and called two
of these tools. See that directory's README for provenance and for why the
fixture is a capture rather than something written here.
