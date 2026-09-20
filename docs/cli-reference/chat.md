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
