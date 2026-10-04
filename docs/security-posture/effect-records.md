# Effect records: intent before, outcome after

Every external effect Cascade performs (clearing the clipboard, redeeming
an approval, rotating a refresh token at an identity provider, spawning an
agent, installing a plugin) leaves two audit records: an intent written
before the effect and a terminal record written after it. This page says
what those records are, how to read them after a crash, and the test every
effect site must carry. The code is `internal/audit/effect.go`.

## The contract

`audit.EffectLog` wraps the audit `*Log`. It adds no method to `*Log`.

| Call | Writes | Refuses with |
|---|---|---|
| `BeginEffect(req)` | the intent record and the `eff:<key>` index row | `ErrEffectExists` (conflict) if the key exists in any phase |
| `ConfirmEffect(h, outcome)` | a `confirmed` record | `ErrEffectNotPending` (conflict) unless `h` names the open intent |
| `FailEffect(h, reason)` | a `failed` record: the effect definitively did not happen | `ErrEffectNotPending` |
| `MarkUnknownOutcome(h)` | an `unknown-outcome` record | `ErrEffectNotPending` |
| `EffectState(key)` | nothing; returns the phase | `ErrTampered` if the index row and its records disagree |
| `PendingEffects()` | nothing; every open intent, oldest first | `ErrTampered` if the index is not exactly what the records replay to |

The key is chosen by the caller: 1 to 128 bytes of `A-Z a-z 0-9 : . _ -`,
derived deterministically from what makes the effect unique, for example
`approval.grant:<request_id>`. A key with `/`, a space, a control byte, or
more than 128 bytes is refused before anything is written.

A begun key is begun for good. After `confirmed`, `failed` or
`unknown-outcome`, a second `BeginEffect` for the same key still refuses.
That is the point: an effect whose intent exists is never re-run blindly.
A site that wants to retry a failed effect derives a new key that says so.

## What is stored

Effect records are ordinary audit records. Two fields mark them:
`effect_key` and `effect_phase` (`intent`, `confirmed`, `failed`,
`unknown-outcome`). Both are sealed in the record hash and chained like
every other field, so editing either is caught by `Verify`, which names
the record's sequence number. Both are omitted when empty, so a record
written before this feature encodes, and hashes, exactly as it did; the
fixture `internal/audit/testdata/pre-effect-log` holds a log written by
the earlier code and is verified byte for byte by
`TestPreEffectRecordsVerify`.

A site with no domain kind uses the one effect kind, `effect.external`.
A site that has a domain kind (for example `approval.grant`) uses it.

The index row `eff:<key>` holds `{"phase","intent_seq","terminal_seq"}`.
It is written in the same store transaction as the record it describes:
the intent row by a create-only compare-and-swap, each terminal row by a
compare-and-swap from the exact intent row. If the transaction fails at
any write, neither the record nor the row survives. Sixteen writers racing
for one key, whether they share one `*Log` or each hold their own as
separate processes would, produce exactly one intent and fifteen
`ErrEffectExists` on the memory store and SQLite
(`TestBeginEffectConcurrentOneWins`); the same test runs against
PostgreSQL under the `postgres` build tag.

A plain `Log.Append` whose event carries `effect_key` or `effect_phase` is
refused, so no caller can forge an intent or a terminal record around the
index.

## Reading the log after a crash

At start-up, a site's recovery calls `PendingEffects`. Each handle it
returns is an intent with no terminal record: the process stopped after
the intent was committed and before the outcome was. The effect may or
may not have happened.

For each pending handle, recovery does exactly one of:

1. **Probe the site.** If the site can tell whether the effect happened
   (the clipboard no longer holds the value; the IdP reports the new
   token), call `ConfirmEffect` or `FailEffect` with what the probe saw.
2. **Otherwise, `MarkUnknownOutcome`.** The record says the outcome is not
   known. The key stays begun, so the effect is not run again by retry.
   An operator reads `unknown-outcome` as "check by hand".

Never call `BeginEffect` again to find out; it refuses, by design.

`PendingEffects` walks and verifies the whole log, replays the effect
records into the index rows they must have produced, and refuses with
`ErrTampered` unless the stored rows match exactly. A row deleted or
altered behind the API therefore stops recovery instead of hiding an
intent. `EffectState` checks only the one row against the records it
names; a deleted row reads as "never begun" there, which is why recovery
must go through `PendingEffects`.

If `BeginEffect` returns a handle together with an error, the intent was
committed but its event-bus notification failed. Do not run the effect;
call `FailEffect` on the handle.

## The crash-injection recipe

Every effect site carries a test that kills a process between the effect
and its confirm. There is no production hook for this; the test re-execs
its own binary. `TestEffectCrashBetweenEffectAndConfirm` in
`internal/audit/effect_crash_test.go` is the reference:

1. The parent runs `os.Args[0] -test.run=^<ThisTest>$` with
   `CASCADE_EFFECT_CHILD=<store path>` (a store under `t.TempDir()`).
2. The child opens that store, calls `BeginEffect`, performs the effect
   against a recording fake (one line appended to a file per run), then
   calls `os.Exit(3)` before `ConfirmEffect`.
3. The parent requires exit status 3 exactly, reopens the store and
   asserts `EffectState == intent` and that `PendingEffects` returns the
   key.
4. The parent runs the site's own recovery (probe, then Confirm or Fail,
   else `MarkUnknownOutcome`), then asserts from stored state: one intent
   and one terminal record, `BeginEffect` on the key refuses, the fake
   recorded the effect exactly once, and `Verify` is clean.

## Who writes effect records

Each owner writes the intent before its effect and the terminal record
after it, and carries the crash-injection test above:

- clipboard clear (P1-SEC-09)
- evidence commit (P1-SEC-21)
- approval redemption (P1-SEC-23)
- presence enrolment, the fingerprint record (P1-SEC-25)
- OAuth refresh-token rotation at the IdP (P1-SEC-17)
- `agent.spawn` (P1-AGT-03), the proxied dial (P1-AGT-13), and start-up
  recovery of pending `agent.spawn` effects (P1-AGT-08)
- plugin effects (P1-PLG-03, P1-PLG-08)
- node effects (P1-NODE-06), which also write the jobs outbox; the outbox
  (`internal/jobs/outbox.go`) stays the job-scoped re-perform mechanism,
  and effect records are the accountability trail

Not an effect record: the custody probe (P1-SEC-28). It writes and deletes
a random probe value in one call; no external state survives either
outcome, and a failed cleanup already refuses.

## Limits

- Until a site above lands, nothing in the shipped binary constructs an
  `EffectLog`. The library is complete and tested; it has no caller yet.
- The effect index is not covered by `Log.Verify`, only by
  `PendingEffects`. Run recovery before serving effects.
- A deleted index row lets `BeginEffect` begin the same key again. `Verify`
  stays clean, even with two intents for the key. `PendingEffects` detects
  it and returns `ErrTampered` naming the second intent. Until the log
  covers the index itself, a caller that replays a signed request must run
  recovery first.
- `ErrEffectExists`, `ErrEffectNotPending` and `ErrAlreadyRecorded` share
  `KindConflict`. Compare them by identity, not by kind. Treat any error
  from `BeginEffect` as "do not run the effect".
