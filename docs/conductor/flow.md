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
