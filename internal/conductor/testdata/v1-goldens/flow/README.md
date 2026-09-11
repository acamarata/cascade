# v1-goldens/flow: provenance

- tool: `git show <tag>:<path>` (read via local clone, no `git show` needed
  since the working tree is already checked out at the tag)
- source repo: `../cascade-v1` (local clone, see
  `.claude/planning/p1/ARCHIVE-MAP.md`)
- tag: `archive/p9-integration`
- date harvested: 2026-08-31
- source files:
  - `crates/cascade-cli/src/cmd/conductor/flow.rs`
    (`parse_verdict`, `aggregate_verdicts`, `loop_should_stop`)
  - `crates/cascade-cli/src/cmd/conductor/flow/tests.rs`
    (the `an_explicit_pass_is_a_pass`, `a_fail_carries_its_reason`,
    `a_bare_fail_is_still_a_fail`, `prose_approval_without_the_marker_is_not_a_pass`,
    `a_malformed_or_missing_marker_is_unclear_not_pass`, `unanimous_passes_clear`,
    `a_single_objection_blocks`, `an_unclear_verdict_blocks_and_is_counted`,
    and `no_reviewers_is_not_a_pass` cases)

## Role of this directory

R-21.218 (21-T0-RULINGS-R21.md §G.1) is BINDING and supplies the NORMATIVE
definition of `ParseVerdict`, `Consensus`, and `LoopStop` implemented in
`internal/conductor/flow.go`. v1 flow.rs is read-only corroborating
evidence only (ARCHIVE-MAP §SPEC-SALVAGE): it proves the general shape of
the problem (a reviewer's free-text output must be reduced to an explicit,
unambiguous marker; an unparsed or absent marker must never count as
approval; an empty reviewer set must never count as cleared) was already
solved once, in v1's own words. No behavior in `flow.go` is DERIVED from
v1 — every assertion in `flow_test.go` cites the R-21.218 table, never a
reading of v1's `parse_verdict`/`aggregate_verdicts`/`loop_should_stop`.

## Where v2 (R-21.218) diverges from v1, deliberately

| Dimension | v1 (`flow.rs`, corroboration only) | v2 (`flow.go`, normative, R-21.218) |
|---|---|---|
| Verdict grammar | `[[VERDICT: pass]]` / `[[VERDICT: fail — reason]]` bracket form, first `[[VERDICT:` occurrence, no line anchor | `^\s*VERDICT:\s*(APPROVE\|REJECT\|NEEDS_CHANGES)\b` per LINE, case-insensitive, first matching LINE wins |
| Verdict set | `Pass` / `Fail{reason}` / `Unclear` (three values) | `VerdictApprove` / `VerdictReject` / `VerdictNeedsChanges` / `VerdictUnknown` (four values, no free-text reason field) |
| Aggregation rule | Unanimity: `cleared == true` only if every reviewer passed and none were unclear | Weighted share: `Consensus` requires a weighted APPROVE share strictly > 0.5 AND no REJECT from a lane priced >= 5.0 |
| Weighting | None — every reviewer counts equally, always | Each reviewer weighted by its lane's `base_shadow_price`, normalized to [0,1] by the input's maximum |
| Loop-stop conditions | `Done` marker, `NoProgress` (output repeats prior round's trimmed text), `RoundCap` (caller-tracked externally) | Identical consecutive verdict SETS (not raw text), `Attempts >= 3` (fixed cap), or `CostAccrued >= CostCeiling` — no `[[DONE]]` marker exists in v2's grammar |
| Cost accounting | Not modeled | `LoopState.CostAccrued`/`CostCeiling` is a first-class stop condition (P1 conductor is metered; v1 was not) |

The unanimity-vs-weighted-consensus divergence in particular is a
deliberate P1 design decision (R-21.218), not an omission: v1's
free-running CLI had no per-lane cost signal to weight by, so unanimity was
its only sound rule. v2 has lane pricing (`base_shadow_price`) available at
the call site, so R-21.218 uses it instead.

## Corroborating fixtures

`v1-parse-verdict-corroboration.json` in this directory reproduces the
literal input/expected-output pairs from `flow/tests.rs`, in v1's own
grammar and verdict set, as a historical record. `flow_test.go`'s
`TestParseVerdict_NormativeTable` does NOT read this file and does NOT
assert against v1's grammar — it asserts the R-21.218 table directly, in
v2's own grammar. This file exists only so a reviewer can see, side by
side, that the general shape of the fail-closed contract ("no marker or
an ambiguous marker is never a pass") is unchanged between v1 and v2 even
though the marker's concrete syntax was replaced.
