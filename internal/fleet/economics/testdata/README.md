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
