# `cascade chat`

Records a conversation turn against the daemon.

```
cascade chat [prompt] [flags]
```

| Flag | Meaning |
|---|---|
| `-m`, `--message` | the prompt, equivalent to the positional argument |
| `--thread` | continue an existing thread by id |
| `--json` | emit `{turn_id, thread_id, content}` |
| `--quiet` | suppress the metadata headers in one-shot output |
| `--private` | create the thread `restricted` |
| `--local-only` | create the thread `local-only` |
| `--topics` | list active topics with thread counts, then exit |
| `--threads` | list threads (topic, id, message count), paginated, then exit |
| `--page` | `--threads`: page number, 1-based (default 1) |
| `--page-size` | `--threads`: threads per page (default 20) |

With a prompt it sends one turn and exits. With no prompt it opens the
interactive terminal UI; under `CASCADE_NO_INPUT=1` with no prompt it
exits non-zero instead, for automation parity.

## What it does today, and what it does not

**It records.** The turn, its segments and the thread row are written to
the conversation domain through the daemon's `chat.append_turn`, and the
turn is echoed to any connected SSE client before the call returns.

**It does not answer.** No daemon component generates an assistant reply
yet, so a successful record is reported as:

```
$ cascade chat --thread notes "hello"
error: unsupported: thread notes turn ce102e69…: cascade chat: message
recorded, but no daemon component generates an assistant reply yet
```

That refusal is deliberate and is the honest shape of the current state:
the turn named in it really is stored, and fabricating reply content to
make the exit code nicer would be the kind of stub this project forbids.
The reply generator is a later ticket; when it lands, this page changes
and nothing else has to.

## It needs a running daemon

```
cascade daemon start   # or: cascade daemon run
```

Conversation state lives in the daemon's own `cascade.db`. Without one,
the command reports a transport failure naming the socket — not a missing
feature.

Until the `chat.*` namespace was registered at the composition root, this
command answered `chat.append_turn: method not found` against a daemon
that was running perfectly well: the adapter, the store and their tests
all existed and nothing called them (R-14.284).

## Threads

`--thread <id>` continues a thread; the id is yours to choose and the
thread row is created on first use. A run with no `--thread` currently
needs one supplied — minting an id for a fresh conversation belongs with
the `cascade_cpa_send` MCP tool's own thread-creation rule (T/S-43.T4),
so that both surfaces mint the same way rather than two ways.

## Privacy modes

`--private` and `--local-only` set the thread's privacy mode, one of the
four §5.16 sensitivity tiers, at the moment the thread is created:

| Flag | Mode | What it refuses at routing |
|---|---|---|
| `--local-only` | `local-only` | every lane that is not on this machine |
| `--private` | `restricted` | every lane whose destination cannot be resolved |
| neither | `restricted` | the same as `--private`; this is the fail-closed default |

The two flags name different tiers, so passing both is a usage error
rather than a precedence rule -- a command that says two things gets an
answer, not a silent choice between them.

A mode is set on the request that CREATES a thread. Passing `--private`
together with `--thread <id>` is refused, naming the thread: an existing
thread's mode is not changed, and answering nothing would leave you
believing it had been.

Not passing a flag is not the same as passing `--private`, even though
both yield `restricted`. Without a flag the CLI sends no mode at all and
the daemon applies the default; with `--private` the thread carries a
mode you chose. The distinction is what an audit of "which threads did
someone deliberately mark" reads.

Enforcement is the router's, not this command's: see
[Thread privacy modes](../security-posture/egress-firewall.md).

## Topics and threads

`--topics`, `--threads`, and `--thread <slug>` are read-only listing
surfaces over the conversation domain's own threads. All three run before
any TTY/`CASCADE_NO_INPUT` check and are TTY-independent themselves
(automation parity): they never open the interactive TUI.

```
cascade chat --topics [--json]
cascade chat --threads [--page N] [--page-size N] [--json]
cascade chat --thread <slug> [--json]
```

**`--topics`** lists every topic the daemon's topic engine has already
filed a thread under, with its turn count:

```
$ cascade chat --topics
code	1 thread(s)
general	1 thread(s)
```

The `thread_count` column is currently always `1` per topic: the topic
engine files at most one thread per topic type (one global bucket per
label, not one per conversation), so `--json` output can rely on that
today, but the field exists per-topic rather than as a fixed total so a
future many-threads-per-topic design does not need a wire-shape change.

**`--threads`** lists the same topic threads, paginated:

```
$ cascade chat --threads --page 1 --page-size 20
code	code	4 message(s)
general	general	1 message(s)
page 1/1
```

There is no separate thread "title" in the conversation domain today
(`Thread.Name` defaults to its own id), so both the slug and title
columns show the topic type — the only real, non-fabricated label
available. `--json` emits `{threads, page, page_size, total_count}`.

**`--thread <slug>`** with no prompt opens a thread by id/slug (any
existing thread, not only a topic thread) and prints its summary:
`--json` emits `{id, slug, title, message_count}`; without `--json`, a
real TTY opens the interactive TUI on that thread, and everything else
(no TTY, or `CASCADE_NO_INPUT=1`) prints the same summary as plain text
and exits — automation never blocks waiting on a TUI it cannot render. An
unknown slug exits non-zero naming the slug.

### What serves this, and what does not yet

`chat.topics_list` and `chat.threads_list` are real daemon-side JSON-RPC
methods (`internal/conversation/topics/rpc.go`, registered from
`cmd/cascade/chat_wiring.go`), reading the same conversation store
`chat.append_turn`/`chat.get_thread` do — never a second storage path.
They live in `internal/conversation/topics`, not beside `chat.append_turn`
in `internal/conversation/adapter.go`: that package already imports
`internal/conversation` for its real `ThreadStore`, so the reverse import
would be a compile-time cycle. `--thread <slug>`'s open mode reaches the
existing `chat.get_thread` door directly rather than a third new method.

**Privacy.** A thread marked `--local-only` never appears in `--topics`
or `--threads` output, from any caller — a thread's privacy tier is read
per row and excluded unconditionally, since nothing at the RPC layer
today distinguishes a "local" caller from an "external-capable" one.

**Not configured yet: automatic topic routing.** `chat.append_turn` does
call into a real topic engine on every turn — `cmd/cascade/
chat_topics_engine.go` composes it and wires it in via
`conversation.SetTopicObserver` (`cmd/cascade/chat_wiring.go`) — but that
engine reports itself "not configured" on every call today, because two
independent pieces it needs are not yet composed anywhere in this tree: a
production `pkg/provider.Embedder`, and a `pkg/provider.Store` handle
threaded through to this composition root (see `chat_topics_engine.go`'s
own `resolveChatSegmenterDeps`/`resolveChatRetrievalStore` doc comments
for exactly what is missing and where). Until both close, `--topics` and
`--threads` report only threads something else has already filed under a
`topic:`-prefixed id — there is no live pipeline auto-filing new ones
from ordinary `cascade chat` turns. This is the same honest shape this
page's own "It does not answer" section already documents for reply
generation: the surface is real, the upstream producer is a later ticket.
