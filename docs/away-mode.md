# Away mode: operator presence and stall notices

Away mode decides whether the operator is at the machine, and it needs two
inputs from the daemon: when the operator was last seen, and which stalled
sessions need attention sooner. Both are small, injected seams in
`internal/notify` (`PresenceSource`, `StallSource`). This page covers their
daemon implementations and how the daemon composes them into away mode.

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

## Composition

The daemon builds one away controller, in the `away` registration (supervised
phase, order 60), over the objects it already owns:

- the one `notify.Service` (its router holds the accumulation gate and its
  notifier delivers the return digest);
- the attested presence tracker described above;
- the fleet runtime's admission controller, which is the machine-idle signal.
  Away mode reads it and never builds a second one;
- the bus stall source described above;
- an away journal in its own namespace (`notify.away`), so the crash-resume scan
  never sees an open away episode as unfinished work.

Three supervised loops run on the daemon's run context and stop with it:
`notify.away` ticks the controller every 30 seconds, `notify.away.stalls` runs
the stall source and `notify.away.reload` applies config reloads. Each shows
as its own row in the daemon's subsystem list.

At start the daemon replays the away journal in journal-sequence order, never
in the order the store returns entries. A daemon restarted while you were away
resumes the newest open episode with accumulation already on, and your return
closes that same episode.

The return digest is compiled per session. Each session gets one notification
addressed to it, private to it, built only from the accumulated items that
session could already see. Items no delivered digest represented go back to the
normal queues.

## Config keys

```toml
[notify]
away_threshold = "30m"
digest_urgent_deeplinks = 10
```

`away_threshold` is a positive duration string. `digest_urgent_deeplinks` is a
whole number, zero or more. An absent `[notify]` table means both defaults.
Any other key in `[notify]`, a value of the wrong type or a value out of range
is refused as a whole, and away mode is not started. Setting these keys with
`cascade config set` is not supported yet; edit `config.toml` and reload.

## Reload

When the daemon accepts a config reload, away mode re-reads `config.toml`,
validates the two keys and swaps them into the running controller in one step.
An episode that is already open stays open; the new threshold applies to the
next evaluation, and a new `digest_urgent_deeplinks` applies to the next
digest. If the file cannot be read or the keys are refused, the running config
is kept and a `notify.reload.rejected` event with the error is published on the
`notify` namespace.

## When away mode is off

Away mode never withholds a notification unless it is running. If an input is
missing or invalid, the `notify.away` row is recorded as skipped with the
reason, no controller exists, and every notification is delivered as usual:

| Reason shown | Cause |
|---|---|
| `unknown [notify] key ...`, or a type or range message | the `[notify]` table is invalid |
| `missing dependency: notify runtime` | no notification runtime |
| `missing dependency: operator presence tracker` | no presence tracker |
| `missing dependency: fleet admission controller` | no fleet runtime |
| `missing dependency: store for the away journal` | no store |
| `missing dependency: event bus for the stall source` | no event bus |
| `away journal replay failed: ...` | the journal could not be read |
| `stall source unavailable: ...` or `reload subscription unavailable: ...` | a bus subscription failed |
