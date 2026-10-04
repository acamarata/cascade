# Conductor FLOW primitives

`internal/conductor/flow.go` implements three pure Go decision functions
for the Conductor FLOW pattern (P1-E11-W3-S23-T6). Their behavior is
normatively defined by ruling R-21.218 (`21-T0-RULINGS-R21.md` §G.1),
which is binding; v1's `flow.rs` is corroborating evidence only, not the
source of these rules (see `internal/conductor/testdata/v1-goldens/flow/README.md`
for the full provenance and the deliberate divergences from v1).

## Verdict grammar and ParseVerdict

`ParseVerdict(raw string) Verdict` scans `raw` line by line for the first
line matching:

```
^\s*VERDICT:\s*(APPROVE|REJECT|NEEDS_CHANGES)\b
```

matched case-insensitively. The first matching line wins; later matches on
subsequent lines are ignored. A `raw` string with no matching line yields
`VerdictUnknown` (the zero value). Prose containing an approval word
without the `VERDICT:` prefix never matches.

`VerdictUnknown` is fail-closed: every consumer of a `Verdict` — including
`Consensus` below — treats it as a REJECT.

## Consensus

`Consensus(in ConsensusInput) Decision` aggregates N reviewers' verdicts,
weighted by their lane's `base_shadow_price`:

1. Each reviewer's weight is its lane's `base_shadow_price`, normalized to
   `[0, 1]` by dividing by the maximum `base_shadow_price` present in the
   input. If every price is `0`, all reviewers get equal weight `1`.
2. The decision is `Approved: true` only when BOTH hold:
   - the weighted share of APPROVE verdicts is strictly greater than `0.5`;
   - no REJECT verdict comes from a reviewer whose lane `base_shadow_price`
     is `>= 5.0` (the high-price veto).
3. A weighted share of exactly `0.5` is a tie and resolves to `Approved:
   false`.
4. `VerdictUnknown` and `VerdictNeedsChanges` both count as REJECT for the
   share computation and the veto rule; only `VerdictApprove` counts toward
   the APPROVE share.
5. An empty reviewer set returns `Approved: false` with `Err` set to
   `ErrInvalidRequest` (the typed zero-quorum error).

`Consensus` performs no lookup of its own: the caller supplies each
reviewer's lane price in `ConsensusInput`, which keeps the function pure.

## LoopStop

`LoopStop(st LoopState) bool` decides whether a FLOW loop should stop.
`LoopState` carries every round's per-reviewer verdict set so far
(`Rounds`), the current attempt count (`Attempts`), and the accumulated
cost against its caller-injected ceiling (`CostAccrued`, `CostCeiling`).
`LoopStop` returns `true` (stop) when ANY of:

- the last two consecutive rounds yielded identical verdict SETS (set
  equality over the per-reviewer verdicts, order-independent, but
  count-sensitive — two APPROVEs and one REJECT is not the same set as one
  APPROVE and one REJECT); OR
- `Attempts >= 3` (the fixed attempt cap); OR
- `CostAccrued >= CostCeiling` (when `CostCeiling` is configured to a
  non-zero value; an unconfigured, zero ceiling never triggers this
  condition on its own).

The ceiling and attempt cap are injected configuration on `LoopState`,
never literals read at call time from a global, and `LoopStop` makes no
`time.Now` call — it is a pure function of its input.

## Wiring status

As of P1-E11-W3-S23-T6, `ParseVerdict`, `Consensus`, and `LoopStop` have
no production caller: the loop-until-dry / adversarial-verify-N /
completeness-critic driver that dispatches models and calls these
primitives is P1-E29-W6-S60-T2 (typed job templates for six work kinds,
including `adversarial`), which depends on this ticket and has not yet
landed. The three symbols are recorded in
`internal/build/testonly-allow.json` against that ticket until it lands.

## Fan-out legs: outcomes, persisted results, re-authorized replay, retention

`FanOut`, `Executor.ExecuteFanOut` and `Executor.ExecuteFanOutResponse`
(`internal/conductor/fanout.go`) take a per-call fan-out id and explicit
collaborators: a `WithPermitFn`, a `JournalAppender`, a `LegResultStore`
and (for `FanOut`) an `AuthorizeFn`. `ExecuteFanOut` passes the executor's
own `Authorize`, the same readiness, validation, classifier, sensitivity
and policy step `Execute` runs, with a refusal audited through the
executor's audit writer. A nil collaborator or an empty fan-out id returns
`ErrConstructionFailed` before any dispatch; an id containing `#` or NUL is
refused as invalid input. The fan-out id keys the journal entity
(`resume.FanOutEntity(id)` = `fanout:<id>`), the leg operation ids
(`<id>#<leg>#<attempt>#<kind>`) and the stored records, and salts the leg
request digest. Leg requests keep the client `TaskID`, so audit and usage
attribution are unchanged.

Per leg:

1. **Replay.** A leg in `completed`, or with a stored record, is a replay.
   The record must exist (`KindNotFound` otherwise) and belong to this exact
   leg request: its digest, fan-out id and leg index must match
   (`KindConflict` otherwise). The leg request then passes `AuthorizeFn`
   again. A refusal is returned, no stored byte is released and the record
   is not touched. Only after authorize passes is the stored `Response`
   (Output, Usage, JobID) returned, with no provider call. A leg with a
   record and no `fanout_leg_done` entry (a crash between the record write
   and the done append) gets the missing done entry appended.
2. **Dispatch.** Otherwise `fanout_leg_started` is appended. Its attempt
   is a create-only slot in the daemon store (namespace
   `conductor.fanout.attempts`, key `<fanoutID>#<leg>#<attempt>`), so every
   writer sharing the store, in one process or across a restart, gets a
   unique attempt; a fourth raw start is refused. The leg runs through
   `withPermit` and `Execute`, and on success the `LegResult` is written
   create-only BEFORE `fanout_leg_done`
   `{leg_index, job_id, attempt, outcome: "ok", result_key, request_digest}`.
   A failure appends `fanout_leg_done` with outcome `failed_terminal` (a
   policy, sensitivity, classifier or invalid-input refusal) or
   `failed_retryable` (anything else) and stores nothing.

Every `AppendLeg` and `LegResultStore` error is returned as the leg's
error. Journal payloads carry the result key and the digest, never model
output. The resume scan counts only outcome `ok` as completed and treats a
`failed_terminal` leg as terminal. A leg with three starts and no `ok` done
is not decided by the scan, because the journal alone cannot tell whether
its result was stored before a crash. The re-dispatch decides it: a
matching stored record is replayed through `AuthorizeFn` with no provider
call and its missing done appended; with no record the start is refused
(`ErrLegAttemptsExhausted`, unknown outcome) and the leg is never sent
again.

Records live in namespace `conductor.fanout.legs` under
`<fanoutID>#<legIndex>` as version-1 JSON carrying the leg request's
`Sensitivity`. `DeleteTask(fanoutID)` removes that fan-out's records and
nothing of another fan-out. Model legs are at-least-once: a leg that
started and never finished before a crash may be sent again, bounded by
the three-start cap.

Wiring status: the fleet adapter that implements `JournalAppender` and
`LegResultStore` over the daemon store is in
`internal/fleet/resume/leg_results.go` and `submit.go`. The daemon's
`conductor.execute` fan-out path that mints the fan-out id, calls it and
applies `DeleteTask` per outcome is not built yet (P1-CORE-19); until then
no production caller persists leg output.
