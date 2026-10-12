# Stall supervision

The daemon watches every live fleet session for a stall: a session that is
active, blocked or already stalled and has shown no progress for longer than
the stall threshold. When it finds one, it walks an escalation ladder, one
rung at a time, until the session moves again or a person is asked.

This page describes what the daemon does and what you can see. The library
behind it lives in `internal/fleet/supervision`; the daemon composition is in
`cmd/cascade/daemon_unix_stall.go`.

## When a session counts as stalled

Progress is the latest of three instants on the session record: its last
update, its last tool call and its last prompt. Sessions registered through
the sessions hook pack (`cascade fleet sessions hook-event`) update these as
the harness works. A session that reaches `idle` or `closed` stops being
watched.

The threshold is `[fleet.supervision] stall_threshold`, a duration between
`1m` and `24h`, default `30m`. It is read once when the daemon starts.

```toml
[fleet.supervision]
stall_threshold = "30m"
```

A value that is not a duration, or is outside that range, does not stop the
daemon. The `supervision.stall` subsystem reports `error` with the reason, and
nothing is supervised until the value is fixed and the daemon restarts.

## Cadence

Two supervised loops run under the daemon's run context and show up in
`cascade status`:

| Subsystem | What it does |
|---|---|
| `supervision.stall` | Follows session and job-gate events on the bus. |
| `supervision.stall.poll` | Checks every watched session every 30 seconds. |

The first time a session crosses the threshold the daemon publishes one
`supervision.stalled` event on the `supervision` namespace for that episode.
Rungs then advance at most once every 10 minutes (the rung delay). Progress
from the session ends the episode and resets the ladder.

If the event subscription dies while the daemon is running,
`supervision.stall` reports `error`, the poll loop stops, and one `error`
attention item with source `subsystem:supervision.stall` is filed so the
loss is visible in the attention queue.

## The ladder

| Rung | Attempts | What happens |
|---|---|---|
| Retry | 2 | A retry directive is queued for the session. |
| Context | 1 | A context directive is queued; the next slice gets twice its budget. |
| Supervisor | 1 | A `stall` attention item is filed for the session. |
| Human | 1 | An L2 approval request for `fleet.session.stall` is filed. |

The retry and context rungs need a delivery channel. With
`[context.hydration] enabled = false` there is none, so those rungs fail and
the ladder moves on to the supervisor and human rungs.

## Directives

Directives wait in a per-session queue until the session's next prompt. The
prompt-hydration hook (`cascade context slice --hook`) sends the session id
from the harness payload, and the daemon drains that session's queue into the
slice. The hook renders them above the context capsule under the heading
`Cascade supervision`, one line per directive, oldest first.

A drain happens only when all of these hold:

- the request names a session, and that session is known, parses, and is not closed;
- the caller is the owner CLI, or a principal bound to that same session
  (`context.slice` is owner-only over the socket today, so the principal
  clause applies only if it is ever routed to principals).

Any other request, including one with no caller identity, gets the slice
without directives and leaves the queue untouched. If the daemon is not
reachable the hook still runs, and the directives wait for the next prompt.
Draining does not reset the ladder: a session that receives its directives
but still makes no progress keeps escalating.

A context directive doubles the slice budget for that one slice, once,
however many context directives were queued.

## Approvals

The human rung files one request in the daemon's approval queue, the same
queue `cascade approval list` reads:

| Field | Value |
|---|---|
| Capability | `fleet.session.stall` (class `local_dev`, registered at daemon start) |
| Level | L2 |
| Action | `fleet.session.stall <session id>` |
| Summary | `<session id> stalled (<kind>) since <time or unknown>` |
| Params | JSON object with keys `session_id`, `stall_kind`, `stalled_since` (Unix milliseconds) and `elapsed_seconds` |

The queue limits Action and Summary to 64 bytes for display. When a session id
is too long to fit, it is shortened with `...` in those two fields only; the
params always carry the full id.
