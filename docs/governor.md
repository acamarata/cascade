# Governor: escalation ladder

`internal/fleet/governor` implements `EscalationLadder`, the four-rung path
a stuck entity (a task or session) walks when its confidence falls below a
configured threshold: **Retry -> Context -> SupervisorTask -> Human**.

## Rungs

`EscalationRung` is a defined `int` enum with four ratified members, in
ladder order:

| Value | Rung | What it does |
|---|---|---|
| 1 | `RungRetry` | Retries the stuck work automatically, no extra context. |
| 2 | `RungContext` | Enriches the entity with additional context, then retries. |
| 3 | `RungSupervisorTask` | Creates a supervisor task to monitor the entity. |
| 4 | `RungHuman` | Notifies a human operator. Terminal rung. |

The zero value is deliberately not a member of the enum. `safeRung` is the
ladder's single fail-closed mapper: every one of the four rungs maps to
itself, and every other value — the zero value, a negative value, or
anything past `RungHuman` — maps to `RungHuman`. This is both the enum's
validity check and the ladder's termination proof: `RungHuman` is a fixed
point, so no sequence of advances can produce a fifth rung or cycle back to
an earlier one.

## Policy

`EscalationPolicy` configures one `EscalationLadder`:

- `MaxAttempts map[EscalationRung]int` — per-rung attempt budget. A rung
  missing from the map reads as a zero cap (fail-closed default).
- `RungDelay time.Duration` — minimum interval between `Advance` calls for
  the same entity. `Advance` itself never sleeps; this is data for
  whatever external scheduler drives it on a cadence.
- `ConfidenceThreshold float64` — the `[0, 1]` confidence level at or above
  which `Advance` treats the entity as no longer stuck and is a no-op.

## Advance algorithm

`EscalationLadder.Advance(ctx, entityID)` does at most one rung's worth of
work per call:

1. Read `entityID`'s current rung from the last `journal.KindEscalation`
   entry (`journal.Store.Replay` filtered to that one kind — the same
   entity's log also carries resume cursors and node-streamed records, so
   an unfiltered "last entry" read would mis-report the rung). No entry
   means the ladder starts at `RungRetry`.
2. Ask the injected `ConfidenceProvider` whether the entity is still stuck.
   An error here is **fail-closed**: treated as below-threshold (escalate),
   never silently skipped. At or above threshold, `Advance` returns `nil`
   without touching any rung.
3. Terminal guard: once a `RungHuman` attempt has run at least once for
   this entity, every subsequent `Advance` returns `EscalationExhausted`
   immediately, without calling `HumanNotifier.Notify` again.
4. Attempt-budget check (see below): if the current rung's attempts are
   already exhausted, advance via `safeRung` before doing any work.
5. Execute the current rung's seam. On success, append a success event and
   return `nil`. On failure, advance to the next rung via `safeRung`,
   append a failure event, and return `ErrEscalationRungFailed` (or
   `EscalationExhausted` if the failure was at `RungHuman`).

### Attempt exhaustion advances regardless of seam outcome

A rung's seam may be called repeatedly across `Advance` calls while
confidence stays below threshold, but never unboundedly.
`EscalationPolicy.MaxAttempts[rung]` caps it, and `Advance` enforces this
cap itself — independently of whether the seam keeps succeeding. When the
last recorded `Attempt` at a rung is `>= MaxAttempts[rung]`, the ladder
advances via `safeRung` before doing any further work at the old rung,
even if that rung's seam has never once failed. This is what stops a
perpetually-succeeding seam (for example a `Retryer.Retry` that always
successfully re-enqueues) from looping forever on the same rung.

## Fail-closed rules

- An unrecognised or out-of-range rung value (including the zero value)
  maps to `RungHuman` via `safeRung` — the most restrictive rung, never a
  permissive default.
- A `ConfidenceProvider` error is treated as below-threshold: `Advance`
  executes the current rung rather than skipping escalation.
- A `journal.Store` read or write failure returns a typed error
  (`ErrEscalationJournalUnavailable`) without advancing the rung counter:
  the next `Advance` call still reads the same last-recorded rung.

## Seam interfaces

`Advance` calls through five injectable seams (`escalation_seams.go`), each
satisfied at the daemon's composition root and stubbed only in tests:

| Seam | Runtime satisfier |
|---|---|
| `ConfidenceProvider` | fleet session state machine |
| `Retryer` | admission controller |
| `ContextEnricher` | retrieval/context package |
| `SupervisorCreator` | task/PBD engine |
| `HumanNotifier` | `internal/notify` |

`EscalationLadder` has no direct import of any satisfier's package;
`NewEscalationLadder` takes all five as constructor arguments, wired at the
daemon entry point.

## Journaled events

Every rung transition (success, failure, or attempt-exhaustion) appends an
`EscalationEvent` to the journal under `journal.KindEscalation`:

```json
{
  "entity_id": "...",
  "rung": 1,
  "attempt": 1,
  "last_error": "",
  "timestamp": "..."
}
```

`Timestamp` is always read from the ladder's injected clock, never a bare
`time.Now()`.

## Throttle ladder

`internal/fleet/governor` also implements `ThrottleLadder` (P1-E13-W3-S26-T3):
a graduated pressure-response ladder built on top of `AdmissionController`
(S-26.T2). It reads `AdmissionController.Pressure()` — the controller's own
`max(inflight/MaxInflight, queueDepth/QueueCap, swapUsedFraction)` metric — as
its only input; the ladder defines no pressure metric of its own.

### `ThrottleStage`

Four rungs, in ascending order: `StageNormal`, `StageWarn`, `StageCritical`,
`StageHalt`.

### `LadderConfig`

TOML-configurable under `[governor]` (`08-INIT-CONFIG-SPEC.md` §3, R-14.42);
every field tolerates its zero value.

| Field | Meaning | Default |
|---|---|---|
| `WarnThreshold` | Pressure at/above which the ladder reports `StageWarn`. | 0.60 |
| `CriticalThreshold` | Pressure at/above which the ladder reports `StageCritical`. | 0.80 |
| `HaltThreshold` | Pressure at/above which the ladder reports `StageHalt`. | 0.95 |
| `StepDownDwell` | How long sustained relief is required before stepping down one rung. Negative normalizes to exactly zero (no dwell); zero (unconfigured) defaults. | 30s |
| `PollHz` | Ladder tick rate, clamped to `MaxLadderHz`. | 1.0 |

`NormalizeLadderConfig` forces the three thresholds into ascending order
(Warn ≤ Critical ≤ Halt), so a misconfigured ladder is never less restrictive
than intended.

### Hysteresis

Escalation (a worsening reading) applies immediately. De-escalation requires
the reading to imply a lower stage continuously for `StepDownDwell` before the
ladder actually steps down, one rung at a time — sustained relief, never a
single good tick, earns a step down.

### `Subscribe` and `Stage`

`Subscribe(ctx context.Context) (<-chan ThrottleEvent, cancel func())`
(18-T0-RULINGS-R16.md R-16.64) registers an in-package listener for every
future stage transition; no consumer is wired at this ticket's composition
point — later tickets (conductor, scheduler, event bus, RPC, doctor) wire
consumers at their own composition points. `cancel()` (or `ctx.Done()`) stops
delivery and leaves no goroutine running. `Stage()` returns the ladder's
current stage; with a nil `PressureSource` it unconditionally reports
`StageHalt` (a missing signal is never treated as a zero reading).

### Effect on admission

The ladder's `Stage` is installed as the `AdmissionController`'s
`StageProvider` seam (R-21.215): `StageHalt` makes `Admit` return
`ErrThrottled` with nothing queued; `StageCritical` halves the controller's
effective `MaxInflight` (integer division, floor 1); `StageWarn` and
`StageNormal` leave admission unchanged.
