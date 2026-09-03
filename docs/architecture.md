# Architecture

Status: seeded from Wave 1 (P1-E03-W1-S04-T3, the typed persistent event
bus; P1-E03-W1-S04-T4, the persisted cron scheduler). This file documents
the subsystems those tickets establish: `internal/events` and
`internal/events/scheduler`. Ticket contracts:
`.claude/planning/p1/phase/epics/E-C/waves/W-1/sprints/S-04/tickets/T-3.yaml`,
`.../T-4.yaml`.
Other subsystems (hooks, doctor, crash recovery, the daemon composition
root) are out of these tickets' scope and are not documented here yet.
Each adds its own section as it lands.

## Event bus (`internal/events`)

`internal/events` is the daemon's typed, persistent pub/sub backbone,
consumed by the scheduler, hooks, doctor, crash-safety, and memory
consolidation (see the ticket's `full_desc` for the full consumer list,
none of which have landed yet as of this ticket).

### Persistence model

The R-14.5 cascade.db domain set is closed at ten members and has no
`events` domain of its own. This package persists through the **`queue`**
domain (`internal/storage/domains.go`'s `DomainQueue`, owned by
`internal/storage/queue`) via `provider.Store`'s namespace-scoping
convention (the same mechanism every domain uses) rather than through
`provider.Queue`'s `Enqueue`/`Dequeue`/`Ack`/`Nack` surface. `Dequeue`
CLAIMS a message for exactly one consumer, which is incompatible with this
package's core requirement: multiple independent named cursors replaying
the same events without stealing them from one another. `Bus` depends on
`provider.Store` directly, exactly as `internal/storage/queue.Queue`
itself does; a composition root wires it to the same domain-scoped Store.

Two record kinds share one `Store` namespace, distinguished by key prefix:

- `event:<20-digit zero-padded Seq>`: one persisted `Event`, encoded by
  `encodeEvent` (`internal/events/types.go`): an 8-byte Seq, an 8-byte
  UnixNano Timestamp, then three length-prefixed sections (Kind, Source,
  Payload). Zero-padding makes lexical key order equal numeric Seq order,
  so `Store.Scan`'s documented "in key order" walk doubles as replay
  order.
- `cursor:<name>`: one named replay cursor's last-committed Seq, an
  8-byte big-endian value (`internal/events/cursor.go`).

A `namespace` argument to `Publish`/`Subscribe`/`Replay` is this package's
unit of ordering (an event log, or "topic"), mirroring
`internal/storage/queue.Queue`'s own per-namespace design. Ordering is
guaranteed strictly within one namespace and NOT across different
namespaces.

### Cursor lifecycle

A cursor's persisted value is the **last committed Seq**: the highest Seq
that has actually been handed to its subscriber's channel. `commitCursor`
is called only after that channel send succeeds
(`internal/events/bus_subscribe.go`'s `deliverLoop`), never before, which
is what makes delivery **at-least-once**: a crash between an event landing
in `Store` and its cursor commit redelivers that event on the next
`Subscribe` with the same name (the cursor is still at its old value); a
crash after the commit never redelivers it (the bus already delivered it).

A cursor is `Subscribe`d by name, "open-or-create at Seq 0": a name that
has never committed reads as 0 ("before the first event") with no error.
`Unsubscribe` stops the live delivery goroutine and channel but leaves the
persisted cursor exactly where it last committed: "release" means release
the live resources, never forget the durable replay position, which is
the entire point of a *named* cursor surviving to the next `Subscribe`
call under the same name, including across a full process restart, kill
-9 included (proved by `TestEventBusReplayCursor` against a real sqlite
`Driver` reopened at the same path).

### Replay semantics

`Replay(offset)` and cursor-driven resume both read as **exclusive** of
`offset`: they return events with `Seq > offset`. This is the only
consistent pairing with "cursor = last committed Seq": resuming at
`cursor+1` never re-delivers the event the cursor already accounts for,
and never skips one either. `Replay` is a bounded, point-in-time read of
whatever `Store` holds at call time; it never blocks waiting for future
events (`Subscribe` is what also tails live).

### Backpressure

`Publish` never blocks on a subscriber: it only depends on the `Store`
write succeeding. Each subscription owns a bounded channel (`Subscribe`'s
`bufferSize`) and its own background delivery goroutine that pulls from
`Store` independently. A slow subscriber's delivery goroutine blocks on
its own channel send until the subscriber drains it or the subscription
is stopped; memory is bounded by `bufferSize`, nothing is ever silently
dropped, and a stalled subscriber never slows down `Publish` or any other
subscription. A dead subscriber (never reads, never unsubscribes) leaks
nothing beyond its own blocked goroutine, which `Unsubscribe`/`Close`
always terminates deterministically.

See the package doc comment in `internal/events/bus.go` for the full,
authoritative statement of every guarantee above.

## Scheduler (`internal/events/scheduler`)

`internal/events/scheduler` is the daemon's persisted cron scheduler,
built directly on the event bus above. CronJob definitions (ID, Spec,
Owner, LastFire) persist through `provider.Store`, the same shared
namespace the event bus uses (`"sched:job:"`/`"sched:lock"` key prefixes
keep records apart from the bus's own `"event:"`/`"cursor:"` keys), so
they survive a daemon restart.

### Job persistence and schedule grammar

A `CronJob.Spec` is either `"@every <duration>"` (a fixed interval, used
for the retention jobs below) or a standard 5-field numeric cron
expression (`minute hour day-of-month month day-of-week`), parsed by
`ParseSpec` (`cron.go`). All five fields must match (AND semantics): this
is a deliberately small internal dialect, not full cron(5) parity.

### Skip-missed scheduling

Every occurrence, at `Activate` and at every subsequent `Tick`, is "the
next occurrence strictly after `clock.Now()`", never a backlog computed
from `LastFire`. A job whose daemon was down across N scheduled windows
fires exactly once for the next valid window, never N times in a burst.
`Scheduler` runs no internal wall-clock timer: `Tick` must be called by
the caller (a real ticker in production, or a test advancing a frozen
clock), which is what keeps every test in this package deterministic with
zero sleeps.

### Advisory lock

Before `Activate` succeeds, `Scheduler` acquires a domain-level,
lease-based advisory lock over the shared `Store` namespace (`lock.go`),
`§D-3`'s "multi-daemon shared-store unsupported" contract. A second
daemon process opening the same namespace fails `Activate` with a typed
`cascade.KindConflict` error; the first holder is unaffected. The lease
(not a lock held forever) is what keeps a crashed holder from permanently
starving every future daemon: it is released explicitly on a graceful
`Close` (including a canceled `Activate` context) or on a fatal in-`Tick`
error (a panicking job's runnable, recovered, never crashes the
process, but treated as fatal to this instance's exclusivity), and
recovered by simple expiry when nothing runs at all (an actual process
death).

### Overrun and orphaned owners

`Tick`'s overrun policy is **SKIP**: a `Tick` call that arrives while a
previous one on the same `Scheduler` is still running returns immediately
without firing anything: never a concurrent double-fire, never a queued
backlog. A persisted job whose `Owner` has no registered `Runnable` (or
whose `Spec` no longer parses) is never silently dropped: it is reported
in `OrphanedJobs()` and published as an `EventKindSchedulerOrphanedOwner`
event on the bus, for a future doctor scheduler check to surface.

### Retention wiring

`RegisterRetentionJobs` (`retention_register.go`) registers
`internal/storage`'s `DomainPruner` and `VacuumJob` as scheduled runnables
at the weekly (168h) default the plan names explicitly, the one
subsystem this ticket wires; no other future consumer (e.g. memory
digest) is pre-registered without a real runnable behind it.

No journal wiring: every event this package publishes goes to the event
bus only. Journal integration is explicitly deferred to `M/S-27.T1`.
