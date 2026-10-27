# Away mode: operator presence and stall notices

Away mode decides whether the operator is at the machine, and it needs two
inputs from the daemon: when the operator was last seen, and which stalled
sessions need attention sooner. Both are small, injected seams in
`internal/notify` (`PresenceSource`, `StallSource`). This page covers their
daemon implementations. The section that wires them into the away controller
is added with that composition.

## Presence comes from attested calls only

Machine idleness is not presence. A long compile emits no event while you are
there, and a scheduled job emits one at 03:00 while you are asleep. Cascade
therefore counts exactly one thing as presence: a call to an elevated verb that
you answered with your enrolled key, and that then succeeded.

`daemon.PresenceTracker` holds the time of the last such call.
`daemon.PresenceMiddleware` feeds it. It is mounted on the daemon registry at
`phaseMiddleware` order 20, inside the elevation middleware (order 10), so the
verified elevation witness is already in the context it receives.

After the handler returns without an error, the middleware records the clock if
and only if the context carries a verified elevation witness for this method
and these exact params. In every other case it records nothing:

| Call | Counts |
|---|---|
| An elevated verb answered with a valid attestation, handler succeeded | yes |
| The same call, handler returned an error | no |
| The challenge (`ELEVATION_REQUIRED`), a refused or replayed attestation | no |
| `inbox.read`, `inbox.ack`, `status.get`, `doctor.run`, any list or show | no |
| `chat.append_turn` and any other agent or hook driven call | no |
| A client-supplied `_attestation` envelope on a verb that needs none | no |
| A session change, a bus event, a stall | no |

Only the elevation middleware can mint a witness: the witness fields are
unexported and its constructor is private to `internal/rpc`, so no other
package can fake presence. The tracker is also bound to the daemon's own
elevation middleware (`PresenceTracker.Bind`, from the verify-only handle that
middleware publishes), so a witness from any other gate does not count, and a
tracker that was never bound counts nothing. The time is read after the handler returns, never
before. The stored instant only moves forward. A tracker with no clock, or a
clock that reports the zero time, records nothing, so the operator stays away.

`LastActivity` returns the zero time until the first attested call.

## Stall notices

`notify.BusStallSource` turns each `supervision.stalled` bus event into one
`notify.StallNotice` for away mode, which uses it to raise the matching
accumulated notification to urgent.

- It subscribes to the `supervision` namespace from its head under the durable
  cursor `notify-away-stall`, so a first start never replays old stalls and a
  restart resumes after the events the bus already handed over.
- The notice carries the payload's `id` (`stall:<session>:<ms|unknown>`) and
  `correlation_id` (the session).
- A repeated `id` among the last 4096 is dropped. A payload that does not
  decode is dropped with one warning; the warning never echoes the payload.
- `DrainStallNotices` returns each notice once. Undrained notices are capped at
  4096; beyond that the oldest are dropped and a warning is logged.
- `Run` ends on cancel. Before it returns it stops the subscription and
  decodes every event the bus had already handed over, because the bus commits
  its cursor when it hands an event over, not when the consumer is done with
  it.

One limit to know about: a notice that is held in memory when the whole daemon
process dies is not replayed on the next start, since the bus has already
committed its cursor. This is accepted because the stall itself is announced
again: on start the stall detector re-seeds every watched session from the
durable session store, begins a new episode for a session that is still quiet,
and publishes `supervision.stalled` with the same notification id. A stall that
ended before the restart needs no notice. Presence and stall notices both lean
toward delivery when in doubt: no presence reading means away.
