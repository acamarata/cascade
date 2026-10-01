# Queue durable state recovery and failed transitions

Restores the queue's "all state persisted" contract.

`internal/storage/queue` is the local, at-least-once `provider.Queue`
driver. Before this change, only a message's payload and attempt counter
were durable (`persistence.go`'s predecessor, `envelope.go`); which IDs
were ready, which were claimed, under what receipt and until what
deadline lived only in the `Queue` instance's own memory. A restart, or a
failed Store operation partway through a claim/Ack/Nack/DLQ transition,
could silently strand or duplicate a message.

## The contract

Every field that decides a message's fate is now ONE Store value per
message (`persistence.go`'s `record`): ready/claimed state, attempt
count, receipt and visibility deadline, alongside the payload. Every
transition — claim (`Dequeue`), `Ack`, `Nack`, DLQ move — is exactly one
`Tx` call on a `pkg/provider.Store`, whose writes are `Tx.CompareAndSwap`-fenced
on the record's prior bytes; a `cascade.KindConflict` aborts the
transition and the caller retries or moves on to the next candidate. No
transition spans two `Tx` calls.

A new `Queue` instance reconstructs a namespace's ready ordering and
inflight claims by scanning `Store` the first time that namespace is
touched (`state.go`'s `namespaceLocked` → `persistence.go`'s
`recoverNamespace`). Scanning "msg:" keys already yields original enqueue
order, because every ID this package generates (`ids.go`) is a
zero-padded monotonic sequence plus a random suffix and `Store.Scan`
walks results in key order — no separate ordering field is stored.

A claim whose deadline passed while no process was running is recovered
as ready at its original enqueue position, ahead of later messages. This
is not an equivalent behavior to "treat every claimed record as inflight":
that variant redelivers a later message first
(`TestQueueRecoveryRedeliversExpiredClaimInEnqueueOrder`). The deadline
boundary is the same in recovery and in the runtime sweep: a claim is
expired when `now >= deadline`, so the window has elapsed at exactly the
deadline (`TestQueueClaimExpiresExactlyAtDeadline`). `Ack` sweeps first,
so a receipt whose deadline passed gets `KindTimeout` and the message
stays stored for redelivery
(`TestQueueAckAfterExpiryIsTimeoutAndKeepsRecord`).

An existing body-only record (the pre-change wire format) is migrated in
place, the first time it is read: a ready-state record is synthesized
(attempts carried over, receipt/deadline unset) and written back via one
`CompareAndSwap` from the legacy bytes to the new format — CAS-fenced the
same as any other write, and idempotent, since migration is a pure
function of the legacy bytes: two instances racing the same migration
compute byte-identical output, so the losing side just re-reads instead
of erroring.

Deletes (`Ack`, and the DLQ move's live-record removal) use the
delete-fencing pattern, because `provider.Tx` has no conditional delete:
`tx.Get` inside the one `Tx`, compared byte-for-byte against the prior
bytes the transition started from (kept in memory since recovery or the
last successful transition, never re-read via a second `Tx`), then
`tx.Delete` on a match — a mismatch returns `KindConflict` before any
delete happens (`TestQueueAckDeleteFencedByPriorBytes`; the public error is pinned to
`KindConflict`, checked by kind and full message). `Ack` never reports `KindTimeout` for a KNOWN receipt whose bytes
changed underneath it — that reads as "stale, safe to retry past," which
is exactly wrong for tampered or concurrently-mutated bytes. `KindTimeout`
stays reserved for a receipt this `Queue` instance no longer recognizes at
all (unknown or already redelivered under a fresh receipt). A storage
failure while reading inside the fenced `Tx` says nothing about the record,
so it surfaces as `KindUnavailable` and the same receipt can be retried
(`TestQueueAckStorageReadFailureIsNotConflict`). A stale claimant whose
claim another instance has since taken over gets `KindConflict` from `Ack`
and `KindTimeout` from `Nack`, and the new owner's record stays
byte-identical (`TestQueueAckByStaleClaimantRefusedAfterReclaim`).

A record whose first four bytes match the versioned format's magic but is
truncated short of a full header is a hard `KindIntegrity` refusal, never
a silent fall-through to legacy migration
(`TestQueue_TruncatedModernRecordFailsClosed`, this change's fail-closed
probe) — the pre-fix `decodeRecord` used a single `||` check for "too
short OR wrong magic," so a short-but-magic-prefixed buffer fell to
`migrateLegacy`, which would read the magic bytes themselves as a bogus
attempt count and deliver the garbage as a normal message.

In-memory bookkeeping is updated **only after** the corresponding `Tx` has
actually committed. This is the direct fix for the audit's exact finding:
the pre-fix `Ack` released its claim's tracking (making the receipt look
unrecognized) before attempting the Store delete, so a failed delete
looked like a stale receipt on retry while the body was still stranded,
undeleted, in Store.

## Proof

- `TestP1QueueRestart`, `TestP1QueueTwoInstances` — real `providers/sqlite`
  storage, closed and reopened, and two independent `*queue.Queue` values
  (each with its own mutex) racing to claim the same message over one
  shared `Driver`.
- `TestQueueRecoversAfterRealProcessKill` — a REAL child OS process
  commits a REAL claim transaction to a REAL sqlite file and is REAL
  SIGKILLed before it can close anything; a fresh `Queue` instance over
  the same file recovers the claim as inflight and redelivers it once its
  deadline elapses. No mock simulates this outcome.
- `TestP1QueueStorageFaults` — a fault-injecting `Store`/`Tx` wrapper
  over a real sqlite file. One subtest per seam: a migration race's
  re-`Get`, the claim CAS ("attempt Put"), receipt generation, `Ack`
  delete, the DLQ record write and the DLQ live-record removal (plus the
  recovery Scan). After the injected failure the stored record is
  byte-identical, and a `Queue` over the reopened file still reaches it
  (delivers it once its claim is free, or finishes the dead-letter move).
- `TestQueue_MigratesLegacyBodyOnlyRecord`,
  `TestQueue_MigrationIdempotentUnderConcurrentInstances` — a seeded
  legacy record becomes deliverable, keeps its prior attempt count, never
  leaks across a namespace, and migrates consistently under two racing
  instances.
- `TestQueueAckDeleteFencedByPriorBytes` — a record tampered with between
  claim and `Ack` makes `Ack` refuse the delete and report `KindConflict`,
  never `KindTimeout`.
- `TestP1QueueAckRetryAfterDeleteFailure` — after an injected `Ack` delete
  failure, retrying with the same receipt reaches a durable outcome
  (success once the fault clears), never a false stale-receipt error while
  the body is still stored.
- `TestQueue_TruncatedModernRecordFailsClosed` — the fail-closed probe: a
  magic-prefixed but truncated record is refused (`KindIntegrity`), never
  silently migrated as if it were legacy.
- `TestP1QueueTwoProcessesOneClaim` — two REAL OS processes, re-executing
  this test binary, over one real sqlite file seeded with one unexpired
  claim, two expired claims and three unclaimed messages. While the first
  process holds the store, the second process's `Open` is refused with
  `KindConflict` (exact kind and message) and it claims nothing. After the
  first exits, the second claims exactly the unclaimed and expired
  messages the first did not take: never the unexpired claim (its stored
  bytes stay identical) and never anything the first process claimed.

## Two-process scope

`providers/sqlite.Open` enforces a single-owner invariant (a
non-blocking exclusive flock on a sidecar `.lock` file) and refuses a
second concurrent `Open` of the same path with `KindConflict`/`ErrLockHeld`
(`providers/sqlite`'s `TestOpen_ExclusiveLockRefusesSecondOpener_CrossProcess`
proves it independently). Two OS processes therefore never overlap inside
`claimLocked`'s `CompareAndSwap` against one sqlite file, and the
two-process test cannot show that CAS matters. `TestP1QueueTwoInstances`
(two `*Queue` values sharing one already-open `Driver`, in one process)
is the test that races a stale in-memory belief against a live CAS:
replacing the claim's `CompareAndSwap` with `Put` turns it red, so no
process mutex can stand in for the Store-level fence.

## Unresolved limits (honest gaps)

- **A retried `Ack` after an ambiguous commit returns `KindConflict`.** If
  the fenced delete's `Tx` commits but the caller sees `KindUnavailable`
  (for example the backend reports a cancellation after the commit), the
  first `Ack` reports `KindUnavailable`. Retrying with the same receipt
  then returns `KindConflict` ("changed since claim"), because the record
  is already gone. The body was deleted, so nothing runs twice. The receipt
  stays tracked in this instance's memory until its deadline passes, and
  the caller should treat that `KindConflict` as "possibly already done".
- **A lost claim or DLQ CAS drops the id from that instance's memory.** A
  message another in-process instance enqueues after this instance
  recovered the namespace is therefore not seen by this instance until it
  restarts; once such a claim expires, nothing on this instance redelivers
  it. The single-owner file lock on the sqlite store limits this to
  instances sharing one process.

- **Capacity accounting is not itself CAS-fenced.** `Enqueue`'s capacity
  check reads the in-memory ready+claimed count before writing; two
  concurrent instances near a namespace's capacity ceiling could both
  pass the check and slightly overshoot it. This is unchanged from the
  pre-change behavior and orthogonal to the claim/Ack/Nack/DLQ durability
  this change repairs.
