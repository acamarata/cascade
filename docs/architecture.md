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

## Notification router (`internal/notify`)

Status: `P1-E23-W5-S49-T1`. Ticket contract:
`.claude/planning/p1/phase/epics/E-W/waves/W-5/sprints/S-49/tickets/T-1.yaml`.
`internal/notify` is the core, strictly-internal notification router: it
aggregates, prioritizes, scope-gates, and fans out `Notification` values
to in-process subscribers. It never sends outbound traffic itself and
holds no knowledge of any specific bridge, channel, or harness — outbound
delivery (e.g. a Telegram bridge) is a separate, later consumer (`S-48`),
not built here.

### Aggregate pattern

`NewService` wires one `Registry` (subscriber bookkeeping), one
`Dispatcher` (priority-ordered fan-out), one `Inbox` (the query
projection), one `Notifier` (the direct producer entry point), and one
`NotificationRouter` (the event-bus decoder) around a shared, unexported
`queueSet` of four buffered channels, one per `Priority`. Two paths feed
the same queues: `NotificationRouter.Run` decodes events off a live
`internal/events.Bus` subscription per the normative source-Kind mapping
table, and `Notifier.Deliver` is the direct producer API (CI fan-out,
delegation results, and future callers) that validates and enqueues
without touching the bus at all.

### Priority ordering

`Priority` is an ordered `int` (`Urgent` < `High` < `Normal` < `Low`), so
`Dispatcher.Drain` drains the four queues in that fixed order every call.
The ordering guarantee is per single `Drain` call, not across calls.

### Deep-link semantics

`Notification.DeepLink` is an opaque `cascade://` URI hint for a
consuming surface to route the user to the right place. The router never
parses, validates, or resolves it — it is carried through unmodified.

### Subscriber contract and delivery ledger

A `Subscriber` (`ID`, `Match`, `Receive`) registers into the `Registry`
paired with a `CandidateSession` (its own session ID plus its precomputed
scope-graph candidate set, from
`internal/context/scope.CandidateScopeRefs` computed by the caller — this
package never calls that function itself, keeping its delivery logic pure
and I/O-free). Before every fan-out, `ScopeDeliveryPredicate` (`scope.go`)
gates delivery per R-16.5: `scoped` requires membership in the candidate
set, `addressed` requires an exact session-ID match, `global-critical`
always delivers, and anything else — including an unresolvable scoped
Notification with no scope fields set — is withheld and counted, never
delivered as a fallback. A per-`Drain`-call ledger (keyed on Notification
ID plus Subscriber ID) prevents dispatching the same Notification to the
same Subscriber twice within one cycle; a Subscriber whose `Receive`
errors is logged and its Notification is queued for redelivery on the
*next* `Drain` call, not fed back into the channel mid-cycle, which would
let the ledger silently swallow the retry in the same pass.

### Inbox is a projection, not a record of truth

Per R-21.227, `Inbox` holds a live, in-memory view only — it persists
nothing across a daemon restart. `Inbox.List/Read/Ack` apply the same
`ScopeDeliveryPredicate` plus a fail-closed `Visibility` gate
(`private`/`scoped`/`shared`/`executive`, unknown resolving to `private`)
before a record is discoverable by id, so a withheld record never leaks
through existence. A producer that needs pending-across-restart semantics
owns its own durable table and re-`Deliver`s on daemon start; this
package makes no durability claim.

## Away mode and return digest (`internal/notify`)

Status: `P1-E23-W5-S49-T2`. Ticket contract:
`.claude/planning/p1/phase/epics/E-W/waves/W-5/sprints/S-49/tickets/T-2.yaml`.
Extends the router above (S-49.T1) with an away-mode state machine
(`AwayController`, `away.go`/`away_config.go`/`away_signals.go`), the
accumulation gate (`accumulate.go`) and a per-session return digest
(`DigestCompiler`, `digest.go`). The full `files_scope` deviation is declared
at the end of this section.

### Signals: two injected interfaces, no invented wire format

`AwayController` consumes exactly two upstream signals, both as narrow
injected interfaces:

| Interface | Question it answers | Real implementation |
|---|---|---|
| `PresenceSource` | when was the OPERATOR last active | arrives with the wiring ticket |
| `AdmissionIdleSource` | is the MACHINE side idle (`Inflight()==0 && QueueDepth()==0`) | `*governor.AdmissionController`, compile-time asserted |
| `StallSource` | which accumulated notification is stalled | arrives with the wiring ticket |

The contract names four upstream symbols that do not exist in this tree:
`supervision.Subscribe`, event Kind `attention.idle`,
`AdmissionController.IdleState()`, and a `supervision.stalled` payload
carrying the stalled notification's id. The nearest real signals answer
different questions, so they are not substitutes: `Inflight()==0 &&
QueueDepth()==0` means the governor has no work, which is machine
idleness, and a session-change event is neither necessary nor sufficient
for presence (a long single-session compile emits none while the operator
is present; a cron-driven session change at 03:00 emits one while the
operator is asleep). This package therefore decodes **no** event Kind for
presence or stalls and presents no proxy as presence. The contradiction is
filed as a PCI; the production implementations of `PresenceSource` and
`StallSource` land with the wiring ticket that constructs the controller.

### State machine and the sustained-idle window

`Active` -> `AwayPending` (presence idle past `away_threshold`) -> `Away`
(the machine side also idle for a full window) -> `Active` (operator
activity). `Tick` is the periodic driver: it drains stall notices, reads
`PresenceSource`, and advances the machine. `HandleEvent` takes a pushed
`ActivityEvent` for a root that learns of activity as it happens.

**Sustained** means both signals held for the WHOLE threshold window, not
at one sampling instant: every admission-busy sample restarts the window,
so a machine busy through the window and idle at the exact Tick instant
does not produce `Away`. Only operator activity leaves `Away`; machine
business never does.

Every state transition, its accumulation flip and its journal append happen
inside **one** critical section. A concurrent `Tick` and `HandleEvent`
therefore cannot interleave into `Active` with accumulation still on (a
permanent notification blackhole, since only an `Away -> Active` edge turns
it off), and cannot journal `KindAck` before its own `KindIntent`.

### Accumulation: gated at the single producer choke point

The accumulation buffer and its gate live on `queueSet` (fields in
`dispatch.go`, methods in `accumulate.go`), not on the router, because
`queueSet.enqueue` is the one call **every** producer reaches: the bus-decode
path (`NotificationRouter.handle`), the direct producer path
(`Notifier.Deliver`) and `Dispatcher.Drain`'s own retry re-queue. A producer
calling `Deliver` at 02:00 while the operator is away is accumulated exactly
like a bus event, and appears in the return digest.
`router.SetAccumulate` / `DrainAccumulated` are the away controller's handle
on that gate. Two callers deliberately bypass it: `router.requeue` (a
re-queue must land in the queue it is being returned to) and
`Notifier.DeliverNow` (a closed episode's return digest must not be buffered
into the next episode and reduced to a count inside that episode's digest).

Stall notices are consumed **only while `Away`, inside the controller's own
lock**. The source's contract delivers each notice exactly once, so draining
it while `Active` — with no accumulation buffer to apply it to — would
destroy it; AC#3 obliges reclassification only while `Away`, so a notice
arriving outside it is left pending for the next away episode instead.
Consuming and applying under the same lock the return transition holds also
means a concurrent return cannot land between the two and drop the notice
with only a WARN. A `StallNotice` naming an accumulated notification
reclassifies it to `PriorityUrgent` in place; a notice matching nothing is
WARNed, never counted as handled. Like `PresenceSource`, the source is
therefore called under that lock: a re-entrant implementation deadlocks
rather than corrupting the buffer.

### Return digest: addressed per session

On `Away -> Active` the controller flips accumulation off, **drains the
buffer inside that same critical section**, journals the return, and then
calls `DigestCompiler.CompileDrained` with that snapshot, which builds **one
digest per candidate SESSION** (registrations are deduplicated by session id: two
surfaces on one session are one session). `ScopeDeliveryPredicate` runs
first for each session, so a digest counts only what that session was
already eligible to receive; everything else contributes an opaque
`withheld` count with no title, scope name, correlation id or DeepLink.
DeepLinks are carried only for in-scope Urgent items, capped by
`digest_urgent_deeplinks`.

Each digest is `Class addressed`, `TargetSession` that session,
`Visibility private`, `Priority Urgent`, `OriginScope` the controller node,
`CorrelationID` the away-episode id, `ExpiresAt` unset. Addressing is what
makes the per-session filtering mean anything: a `global-critical` digest
is deliverable to every session by the predicate's own rule, so a digest
built for one project's session while carrying its DeepLinks would reach
another project's session — the cross-scope leak R-21.227 exists to
forbid. The fix needs no new `Notification` field and no `scope.go` change:
`addressed` + `TargetSession` already reaches exactly one session through
the ordinary predicate.

An **empty** drain delivers no digest at all: with nothing accumulated and
nothing withheld there is no summary to give, and a Priority-Urgent "no
notifications while away" per session on every quiet return is noise.

The snapshot is what makes the digest survive an immediate re-entry. Read
after the unlock instead, a concurrent `Tick` pair that re-enters `Away`
turns the gate back on, and a compiler consulting the live gate then refuses
the digest of an episode that has **already returned** — AC#4 failing with
nothing but an ERROR log. `CompileDrained` therefore ignores the gate and
delivers through `DeliverNow`. The bare `Compile` entry point, for a caller
that has not snapshotted, still refuses (typed error, nothing drained) while
accumulation is on: the live buffer belongs to an open episode, so draining
it would take another episode's items and summarise an episode that has not
returned. With no `DigestCompiler` wired at all, the drained snapshot is
re-queued into the normal queues rather than discarded.

### No silent discard

An accumulated item leaves the buffer for good only when a digest that
**actually delivered** counted it. Everything else is re-queued into the
normal per-priority queues, unchanged in priority, class, scope and expiry:

| Situation | Outcome |
|---|---|
| item in a delivered digest's scope | represented; not re-queued (no double count) |
| item withheld from every registered session | re-queued; the `withheld` count reports it, it does not consume it |
| one session's digest fails, another's succeeds | only the items no delivered digest represented are re-queued |
| every digest fails, or no session is registered | every drained item is re-queued |

### Journal and resume

Away-entry and Active-return are journaled via `M/S-27.T1`'s entity journal
as a `KindIntent`/`KindAck` pair sharing the away-episode id as
`OperationID`, entity id = the local node identity. `ReplayState` returns a
`ReplayResult`, and `NewAwayControllerFrom` installs it: a replayed `Away`
state resumes accumulating **before the first Tick** and keeps the
pre-restart episode id, so the eventual return writes its `KindAck` against
the episode the pre-restart `KindIntent` opened. Two kills leave two open
episodes, so the **last** unmatched `KindIntent` wins; returning the first
would resurrect a stale episode forever. A replayed `Away` with no episode
id is refused and downgraded to `Active` — an episode that can never be
closed must not resume accumulating.

The journal records state transitions, not buffer contents, so a resumed
episode starts with an empty accumulation buffer and its digest summarises
only post-restart notifications. That is a recorded limit, not an oversight.

### Configuration

`AwayConfig` holds `away_threshold` (duration, default `30m`) and
`digest_urgent_deeplinks` (int, default `10`), both hot keys. `Validate`
is the validate-before-apply step (08 §3) and is proven on the struct; no
TOML loader in this tree reads a `[notify]` section yet for either this
ticket's keys or S-49.T1's own `Config`, so the loading half belongs to the
wiring ticket.

### `files_scope` deviation (declared once, here)

The ticket's `files_scope` lists four added files (`away.go`, `digest.go`,
`away_test.go`, `digest_test.go`) and one changed (`router.go`). The shipped
change is wider, and every extra file is a split or a named change rather
than new scope:

| File | Why it exists |
|---|---|
| `away_config.go` | `AwayController`'s injected seams (`AwayDeps`, `AwayConfig`, the three source interfaces, `ReplayState`) — split so `away.go` stays under the 300-line cap |
| `away_signals.go` | the same controller's collaborator-facing halves (stall drain, digest hand-off, journal append) — same reason |
| `accumulate.go` | `queueSet`'s accumulation-buffer methods, split out of `dispatch.go` (which was at 299 lines, so the next edit there would have reddened the cap) |
| `notifier.go` | `Notifier` (`Deliver` + this ticket's `DeliverNow`), split out of `inbox.go` for the same reason; `inbox.go` keeps the consumer surface |
| `dispatch.go` | changed under the T0 ruling that moved the accumulation gate to the single producer choke point (`queueSet.enqueue`) |
| `inbox.go` | changed by the `notifier.go` split only |
| `away_accumulate_test.go`, `away_concurrency_test.go`, `away_fixture_test.go`, `away_replay_test.go`, `away_signals_test.go`, `digest_requeue_test.go` | test splits of `away_test.go` / `digest_test.go`, each under the same cap |

### Not yet wired to a composition root

`internal/notify` has zero production callers anywhere in the tree, and **no
P1 ticket owns away-mode wiring**: `AwayController` appears in exactly one
contract, the one that builds it. The four
`internal/build/testonly-allow.json` entries (`NewAwayController`,
`NewDigestCompiler`, `DefaultAwayConfig`, `ReplayState`) say so plainly and
are parked at `P1-E37-W8-S73-T2`, the nearest inbox surface, until planning
forges a real owner in response to the PCI.

## Fused cross-domain recall (`recall.what`, `internal/retrieval/recallwhat*.go`)

`recall.what` (P1-E22-W5-S47-T1, R-14.65/66) is the multi-domain answer to
one query: files, memory, conversation turns and threads, fused into a
single ranked, cited response. It is a composition on top of three
existing surfaces (Epic F's retrieval index, Epic G's memory store, Epic T's
conversation store), not a new index of its own.

### Scope resolution (E/S-08.T4)

Every `recall.what` call resolves the caller's `scope.SessionScope`
server-side, at the composition root
(`cmd/cascade/daemon_unix_recall_what.go`), through the SAME resolver
`context.scope.show` uses: `context/scope.ResolveSessionScope`, over the
persisted deny-by-default scope graph (`scope.GraphStore`). A caller may
still send a `scope` field on the wire, but it is only ever CHECKED against
the resolution, never trusted on its own — a mismatch is refused with
`KindInvalidInput` (`internal/retrieval/recallwhat_scope.go`). This is
deliberately the same posture F/S-11.T1's `fusion.ScopeFilter` documents
for the bare `recall` command: "there is exactly one scope-enforcement
mechanism... and it runs BEFORE any leg does."

The resolved scope reference narrows the files leg (passed through to
`recall.Service`'s own `fusion.ScopeFilter`) and the memory leg
(`IndexedRecord.ScopeRef` equality). The conversation leg cannot be
narrowed this way at all: `conversation.Thread` carries no `ScopeRef`
field, and `conversation.SearchFilter` carries only `ThreadID`/`Limit` — no
scope column exists to check. A caller-side confirming review reproduced
the leak this would otherwise cause (a public thread created under one
project, reachable from a `cascade recall what` call whose cwd resolves to
a different project), so the leg does not run unscoped: **it is
unconditionally excluded**. Every `recall.what` call gets
`Errors["conversation"] = "recall.what: the conversation domain cannot be
narrowed to a session scope"` (`KindUnavailable`), and no conversation row
ever reaches a response, regardless of the thread's own privacy tier or
the turn's role — `SearchTurns`/`ThreadPrivacy`/`ListSegments` are never
even called. This is a per-request refusal, not "leg absent": a build with
no conversation store configured at all reports the leg simply not
configured (no error), a different signal. A widening ticket is tracked
(PCI, repo `cascade`, type `planning`) to add a `ScopeRef` to `Thread` and
`SearchFilter` and restore the leg once one exists.

### Multi-domain fan-out and RRF merge

`RecallWhatService.Query` fans out to three domain legs concurrently
(bounded by a local semaphore, `maxParallelLegs` — no Conductor governor
transit, R-14.66), each producing an `rrf.RankedList`. One leg's failure
never fails the whole query: it is recorded per-domain in `Errors` and the
answer degrades to whatever domains did answer (`KindUnavailable` only
when every domain failed). The three lists are merged by the same
`internal/retrieval/rrf.FuseWith` reciprocal-rank-fusion pass every other
fusion path in this tree calls — no second ranking algorithm.

### TRUST and privacy filtering — the real egress boundary, not a caller claim

Two exclusion rules run on the fused rows before they are described,
`internal/retrieval/recallwhat_filter.go`:

1. **Untrusted-source content is excluded unconditionally.** A
   `corpus.TrustUntrustedSource` row (an externally-sourced file, or a
   tool-authored conversation turn) never reaches a `recall.what` answer,
   regardless of who is asking. This is deliberately tighter than "excluded
   from a privileged caller": the caller-declared-tier version of this
   rule was the exact finding of an adversarial review of this surface's
   first draft (a caller could assert its way past the exclusion by
   claiming a permissive tier), so the rework removed the caller-supplied
   tier from the wire entirely and made the rule unconditional instead.
2. **A conversation thread whose own privacy tier the real egress class
   does not admit is excluded.** The decision is taken by
   `internal/hooks/egress.Engine.InterceptClass` against a dedicated class,
   `EgressClassRecallWhat` (`internal/hooks/egress/classes.go`), configured
   to admit only `internal`/`public` tiers — never local-only or restricted,
   for any caller. The SAME `InterceptClass` call also performs the
   exact-value substitution pass (H/S-16.T1), so exclusion and redaction
   are one call, not two mechanisms that could disagree. **This rule is
   presently unreachable in production**, since the conversation leg is
   itself excluded upstream (the scope-resolution section above) — it
   stays in place, tested directly against `filterFusedResults`, so the
   moment a future ticket restores the leg, tier exclusion and redaction
   are already correct rather than silently missing.

Every outbound string — snippet, path, the chunk id ("memory key"), each
domain's error text, and the rendered citation footnote block — transits
`InterceptClass` at `TierInternal` (the tier the class is always
configured to admit) before it reaches the wire
(`internal/retrieval/recallwhat_redact.go`). A refusal there fails the
whole request rather than silently omitting one field, because a refusal
on an always-admitted tier means the firewall itself is unavailable, not
that one row is sensitive.

The files leg's own citations (`recall.Service`'s `authorize()` +
`citations.Assemble` pass, already run against the real `ScopeFilter`) are
reused verbatim rather than re-derived; a file row that leg's own
authorization withheld is never re-admitted here.

### R-16.7: superseded and expired memory ranks below its peers

The memory leg carries `Supersedes` and `ExpiresAt` through from
`MemoryEntry` into the projection's read model
(`internal/memory.IndexedRecord`, extended by this ticket). After fusion,
`demoteSupersededAndExpired` reorders the RRF output as two stable
partitions: an expired entry sinks below every non-expired peer, and a
superseded entry is reinserted immediately after the entry that supersedes
it — nothing is re-scored, only reordered. A golden fixture
(`internal/retrieval/testdata/v1-goldens/recallwhat_ranking.json`) pins the
expected order against a hand-traced scenario.

This depends on the memory leg actually being HANDED an expired row to
demote in the first place. `ProjectionJob`'s ordinary `Search` excludes an
expired row outright (the same rule the bare `cascade recall` surface
needs — a row past its TTL should not surface there at all), which means
`recall.what`'s memory leg cannot call `Search` and still deliver this
section's own demotion contract: there would never be an expired candidate
to demote. The memory leg therefore calls a second, narrower method,
`ProjectionJob.SearchIncludingExpired` — identical to `Search` except it
does not apply the TTL filter (a genuinely retired/tombstoned row is still
excluded either way; expiry and retirement are different facts). Adding
this method changed `IndexedRecord`'s effective field set requirements
(the `Supersedes` field above already had), so `internal/memory.schema.go`'s
`ProjectionVersion` moved from 1 to 2 — a version a stored row was written
under is a version whose rows must be rebuilt, never compared byte-for-byte
against a different layout's assumptions.
