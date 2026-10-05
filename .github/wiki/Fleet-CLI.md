# Fleet CLI

The `cascade fleet` command group inspects the local fleet: harness
sessions (`fleet sessions`), a live dashboard (`fleet top`), and an
entity's durable journal (`fleet journal show|replay`). A hidden
`fleet sessions hook-event` command registers live sessions from hooks.

> This page currently documents `fleet journal show|replay` (P1-E13-W3-
> S27-T4) and daemon mode below (P1-E12-W6-S121-T1). `fleet sessions`'s
> and `fleet top`'s remaining flags ship in the same command group; see
> their own `--help` output and `cmd/cascade/fleet.go`/`fleet_watch.go`/
> `fleet_top.go` until this page is extended to cover every flag.

## Daemon mode: `fleet sessions [--watch]` and `fleet top`

Against a running daemon (`cascade daemon run`), `cascade fleet sessions`
(one-shot) and `cascade fleet top` both read `fleet.sessions.list` — the
daemon's real fleet-sessions RPC method, registered on the daemon's own
`*rpc.Registry` at startup. `cascade fleet sessions --watch` and the
harness watch both subscribe to `GET /events?topic=fleet.sessions`, the
daemon's SSE endpoint's `fleet.sessions` topic: a topic-dispatching mux
(`internal/rpc.SSEMux`) in front of that endpoint first refuses (HTTP
403) a peer that is not the daemon owner or a browser-shaped request,
then routes a request with no topic to the daemon-wide event stream and
a request with exactly one `topic=fleet.sessions` to the sessions SSE
stream. Any other topic, an empty or repeated `topic`, or a malformed
query is refused (HTTP 400) before either stream's own subscribe logic
runs; an unrecognized topic is never served the wrong stream. Windows tier-2 (no daemon at all)
and a socket that is not reachable both produce a typed refusal, never a
panic; `fleet sessions` (one-shot only, not `--watch`) additionally falls
back to a live embedded read with no daemon at all.

## `cascade fleet journal show <entity>`

Reads `<entity>`'s journal entries from the daemon, in sequence order.

| Flag | Meaning |
|---|---|
| `--after <seq>` | Only entries with a sequence number greater than `<seq>`. Omit to read from the start. A value past the entity's current head is refused (typed error), not silently treated as "no entries". A negative value is refused before the request ever reaches the daemon. |
| `--limit <n>` | Cap the number of entries returned. The daemon clamps any value above **500** down to 500 — this is never an error to the caller, just a silent cap. Omit for no client-requested limit (still subject to the 500 clamp only if you ask for more). |
| `--json` (global flag) | Emit the [versioned `--json` envelope](cli-output-contract) instead of a table. |

**Output formats:**

- **TTY table** (default on an interactive terminal): columns `SEQ`,
  `KIND`, `OPERATION_ID`, `TIMESTAMP`, `PAYLOAD`.
- **`--json`**: the standard envelope (`version`, `ok`, `data`), where
  `data` is an array of `{seq, kind, operation_id, timestamp, payload}`
  objects — the same fields as the table, one JSON object per entry.

**`payload` is always redacted** before either output format renders it:
every entry's payload passes through this repo's existing §D-31 secret
detector (`internal/doctor.RedactText`) first. A secret-shaped value
never reaches the terminal or the JSON output, table or `--json` alike.

**Error codes:**

| Condition | Result |
|---|---|
| Entity has never had anything appended to its journal | Typed `not-found` error — never an empty list. |
| Entity is known but has no entries past the cursor | Exit 0, empty list — not an error. |
| `--after` names a sequence past the entity's current head | Typed `invalid-input` error. |
| `--after` is negative | Typed `invalid-input` error, caught before the daemon is ever dialed. |
| No daemon reachable | Typed `unavailable` error suggesting `cascade daemon run` — the journal has no offline/embedded fallback; it is a daemon-owned durable log. |

## `cascade fleet journal replay <entity>`

Re-emits `<entity>`'s journal entries, in the same sequence order `show`
would return. **Replay never re-executes anything**: it reconstructs and
displays what already happened (escalations, notifications, anything the
journal recorded) — it never re-runs an escalation, re-sends a
notification, or writes anything back to the journal it reads. Calling it
twice in a row returns the identical result both times.

| Flag | Meaning |
|---|---|
| `--from <seq>` | Same semantics as `show`'s `--after`. |
| `--stream` | Render one NDJSON line per entry instead of a single batched result. **This is not a live subscription** — see the note below. |

> **`--stream` is not a live event feed.** Journal replay is a historical
> read of entries that already happened; there is nothing "live" for a
> future event to report that a second `replay` call would not already
> see. `--stream` renders the same batched `fleet.journal_replay`
> response as one NDJSON line per entry (the same wire format `fleet
> sessions --watch`'s non-TTY path uses) rather than opening a live event
> stream. If a future ticket adds a genuine live journal-change
> subscription, it will be a distinct, separately documented capability.

## `cascade fleet bench <lane-id>`

Probes/benches a provider lane's health through the daemon's
`fleet.bench_lane` method: `N` probes at a given concurrency, reporting
p50/p95 latency, error rate, and a rough cost estimate.

| Flag | Meaning |
|---|---|
| `--n <count>` | Probe count (default 1). |
| `--concurrency <count>` | Probes in flight at once (default 1). |

This is a one-shot, headless command — it never opens the daemon's SSE
stream itself — but it still requires a **running daemon**: a probe
dispatches through a real provider lane, which only the daemon's
composition root can construct. There is no offline/embedded fallback
(unlike `fleet sessions`). On Windows (tier-2, no daemon at all) and when
no daemon socket is reachable, it returns a typed refusal, never a panic.

### Lane health

Each successful bench is recorded as its lane's **latest** reading (not a
history) and, when the reading changed from what was last recorded, is
published on the daemon's `fleet.sessions` event stream as a
`fleet.sessions.lane_health.changed` event — a distinct event type on the
SAME stream `fleet.sessions.changed` session events use, not a second
stream and not a field folded into a session's own record (one session
record describes one session; lane health describes lanes, an unrelated
cardinality). A consumer that only wants session state can ignore an
event type it does not need on the same connection.

A lane that has never been probed reports every numeric field at zero
rather than omitting the field, so a reader can always distinguish "this
lane's health is not yet known" (a lookup that returns nothing) from "an
all-zero reading" (a field that is present and reads 0) at the response
shape level.

## Live session registration: `fleet sessions hook-event <Event>`

A hidden command that registers and tracks a live harness session. The
cascade-claude hook pack (`sessions`, hook pack version 2) installs it for
exactly five harness hook events:

| Event | Effect on the stored session |
|---|---|
| `SessionStart` | creates the session, state `active` |
| `PreToolUse`, `PostToolUse` | touch it (last tool time, tool count) |
| `Stop` | moves it to `idle` |
| `SessionEnd` | moves it to `closed` |

Each installed hook runs `cascade fleet sessions hook-event <Event>` with a
5 second harness-side timeout. The command reads the harness's own hook JSON
from stdin (at most 1 MiB), takes `session_id` and `hook_event_name` from it
(other fields are ignored), and posts one `fleet.sessions.hook_event` to the
daemon through the client SDK with a 1 second deadline. The daemon socket is
resolved when the hook runs, the same way every other `cascade` command
resolves it. The recorded harness name is `claude-code` and the PID is the
hook process's parent.

The command sends nothing when the event argument is not one of the five,
stdin is empty or not a JSON object, `hook_event_name` differs from the
argument, or `session_id` does not match `^[A-Za-z0-9_-]{1,128}$`.

It never blocks the harness:

- It exits 0 on every path and never 2.
- Stdout is always empty.
- A failed delivery (no daemon, or a daemon that refuses the event) prints
  one line on stderr, `cascade: fleet session hook not delivered: <kind>`.
  A refused input prints nothing.

`cascade fleet sessions` (and `--json`) then lists the registered session.
A daemon that does not serve `fleet.sessions.hook_event` answers every
delivery with an error, so nothing is registered and the stderr line names
the kind.

The fixtures behind the five events are real captures; their provenance is
in `internal/fleet/hookpacks/testdata/README.md`.

## Hidden alias

`cascade journal show|replay <entity>` is a hidden top-level alias for
`cascade fleet journal show|replay <entity>` (it does not appear in
`--help`, but resolves identically — it is built from the exact same
command constructor, not a second implementation).

## Examples

```console
$ cascade fleet journal show task-42
SEQ  KIND    OPERATION_ID  TIMESTAMP                     PAYLOAD
1    intent  op-1          2026-09-11T00:00:00Z           {"step":"start"}
2    ack     op-1          2026-09-11T00:00:01Z           {"step":"start"}

$ cascade fleet journal show task-42 --after 1 --json
{
  "version": 1,
  "ok": true,
  "data": [
    {"seq": 2, "kind": "ack", "operation_id": "op-1", "timestamp": "2026-09-11T00:00:01Z", "payload": "{\"step\":\"start\"}"}
  ]
}

$ cascade journal replay task-42
SEQ  KIND    OPERATION_ID  TIMESTAMP                     PAYLOAD
1    intent  op-1          2026-09-11T00:00:00Z           {"step":"start"}
2    ack     op-1          2026-09-11T00:00:01Z           {"step":"start"}
```
