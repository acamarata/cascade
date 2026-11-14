# internal/fleet/economics testdata provenance

`goldens/shadow_price_table.json` and `goldens/unknown_pressure_table.json`
(P1-E41-W9-S79-T1) are authored by hand from
`21-T0-RULINGS-R21.md` sections B.3 (R-21.30 lane classes and base shadow
prices, R-21.31 the reserve barrier and SP = B x Q x P_mode x R) and E
(R-21.118 the absolute floor, R-21.128 unknown-source pressure). They are
not captured from an external provider counterpart -- there is no such
counterpart for a pure arithmetic contract -- and they are not a second
copy of the R-21.30 base-price table already pinned by
`internal/fleet/topology/testdata/lane_class_prices.golden.json`: each row
names a `lane_class` and the tests read that lane's real base price from
`topology.BaseShadowPrice` at run time, never a re-typed number.

`shadow_price_table.json` covers, across the seventeen R-21.30 lane
classes: the abundant region, both reserve-barrier boundary values, the
soft-band interior, the hard-reserve boundary, the absolute-floor boundary
and a case below it, and the pressure clamp's own min and max (an
ahead-of-budget bucket near its window reset, and a badly-behind-budget
bucket), so the shadow-price formula is exercised across every region its
two composed inputs (`topology.DomainPressure`, `ReserveBarrier`) can
produce.

`unknown_pressure_table.json` (R-21.128) pins that a lane whose only
bucket is unknown-source prices strictly higher under a sustained
`ewma_429` than the same lane with no 429 history, is never priced at
the superseded flat `1.0`, and stays within the fail-closed clamp -- so
it never reads as unlimited capacity.

Every row's expected pressure and reserve-barrier value is recomputed
independently in the test from the real, already-landed
`topology.DomainPressure` and this package's own `ReserveBarrier`, never
duplicated as a second literal table; the ReserveBarrier boundary values
themselves are separately pinned as exact literals in
`shadow_price_test.go`'s `TestReserveBarrierBoundaries` /
`TestReserveBarrierAbsoluteFloor`, hand-derived from the R-21.31/R-21.118
formulas, not copied from any other file.

`goldens/tokens_out_defaults.json` holds the nine per-task-class default
output-token counts `TokensOutDefault` returns (classify, segment,
summarize, extract, chat, code, reason, review, arbitrate). It is authored
by hand from the reservation estimate rules, not captured from a provider:
the numbers are policy defaults, not an observation of any counterpart.
`reserve_estimate_test.go` checks the table in `reserve_estimate.go`
against it row by row, so a changed default must change both.

## Lane ranking: `rank_table.json` and `rank_goldens.json`

Both files are authored by hand from the R-21 rulings, not captured from
a provider: ranking is a pure function with no external counterpart to
record. Where a ruling constant and `tables.go` or `correlation.go` could
ever disagree, the code wins and the file is corrected to match.

`rank_table.json` (R-21.33 structural correlation) has three parts, read
by `correlation_test.go`:

- `correlation`: seven rows, one per structural cell (same session, same
  model, same family on the same provider, different family on the same
  provider, same model on a different provider, different family on a
  different provider) plus the two-hosts-one-model row. Each row holds the
  producer, the candidate, the expected penalty and its learned floor.
  The penalties (1.00, 0.90, 0.75, 0.50, 0.70, 0.30) and floors (1.00,
  0.80, 0.50, 0.35, 0.80, 0.15) equal `correlationCell` in
  `correlation.go`.
- `precedence`: five rows for the rules that run before the cell lookup
  (a nil producer gives 0, an unknown family on either side gives 1, an
  empty canonical id on either side gives 0.75). `TestCorrelationPrecedence`
  and `TestCorrelationDefaults` check them.
- `learned_floors`: the four relationship floors (same model 0.80, same
  family 0.50, same provider 0.35, different provider 0.15) that bound a
  learned pair value from below. `TestCorrelationLearnedFloors` also pins
  them as literals, so a changed floor must change both.

`rank_goldens.json` (R-21.120 six-term utility and the rank order) holds
five rows read by `TestRankUtilityGolden`. Each row sets one candidate's
quality, shadow price, latency, failure and 429 rate, ranks it against an
identical twin with a later lane id, and pins the six weighted term
contributions, the utility and the order. The rows cover the build, verify
and incident weight rows of `tables.go` (`utilityWeights`), the default
independence weight (0.25 for build, 1 for verify), a producer and no
producer, an equal-utility equal-price pair (lane id breaks the tie) and
an equal-utility pair separated only by shadow price once the scarcity
weight is overridden to zero. Every contribution was worked out by hand
from the weight row and the term formulas in `rank_terms.go`
(for example build-six-terms: quality 5 x 0.8 = 4; correlation
0.50 x 0.25 x 1.0 = 0.125; latency 0.5 x 0.4 = 0.2; failure 1.0 x 0.15;
risk 1.0 x (2 x 0.1 + 0.2 x 0.4) = 0.28; utility 3.245). The shadow-price
term is log1p(SP) times the scarcity weight, so the equal-price row's
-1.2476649250079015 is 1.8 x ln 2.

`rank_integration_test.go` uses no golden file: it builds candidates from
`topology.BaseShadowPrice`, the live `NewTables` multipliers and
`ShadowPrice`, ranks them through the sealed builtin registry and
recomputes the shadow-price and correlation terms from their public
functions.
