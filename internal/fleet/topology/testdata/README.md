# internal/fleet/topology/testdata

## Fixture provenance (P1-E40-W9-S77-T1)

- `lane_class_prices.golden.json` -- the R-21.30 seventeen-row base-shadow-
  price table, transcribed verbatim from the ruling text in
  `21-T0-RULINGS-R21.md`. `TestBaseShadowPriceGolden` (lane_class_test.go)
  asserts `BaseShadowPrice` against every row, in the ruling's own
  declaration order, so an accidental edit to `laneClassPrices` in
  `lane_class.go` shows up as a visible diff against this file rather than
  silently changing behavior.

- `lane_identity.golden.json` -- three fixed input tuples for
  `DeriveLaneID`/`LaneIDFor` (R-21.124: first 16 hex characters of
  sha256 over the five "|"-joined components). Each `want_lane_id` value
  was computed independently with Python's `hashlib.sha256` over the exact
  same joined string the production code builds (see
  `lane_identity_test.go`'s `TestLaneIDDerivation`), not by capturing the
  Go function's own output -- an implementation bug in `DeriveLaneID`
  would therefore produce a mismatch against this file, not a
  self-confirming match. One row (`rp-3`/`dom-3`) has both `canonical_id`
  and `model_id` empty, exercising the "third component is empty" edge
  case explicitly.

- `reconcile_roundtrip.golden.json` -- the expected Account/QuotaDomain/
  RuntimeProfile/Credential/Lane derivation for one fixed
  registry.ProviderRecord + registry.LaneRecord pair (an oauth-authenticated
  `anthropic-1` provider with a single `claude-opus` lane), computed by
  hand from `full_desc`'s derivation rules in the ticket contract. The
  `lane.id` value was computed the same independent way as the
  `lane_identity.golden.json` rows above.
  `TestReconcileMatchesGoldenDerivation` (reconcile_test.go) runs the real
  `Reconcile` function against a REAL `*registry.Registry` (backed by a
  real modernc SQLite file under `t.TempDir`, never an in-memory-only
  stand-in) seeded with that exact provider/lane pair, and compares the
  resulting stored rows against this file field by field.

## Real-counterpart tests with no static fixture

`migration_test.go`'s `TestMigrationRealSQLite` needs no fixture file: its
"real counterpart" (Art.2) is the real `modernc.org/sqlite` driver
(imported directly, the same import `internal/providers/registry`'s own
`migration_test.go` uses) opened against a real file under `t.TempDir()`,
never `:memory:`. `MigrationSet()` is applied and re-applied against that
real file to prove idempotence.

`reconcile_test.go`'s `TestReconcile*` tests likewise build a REAL
`*registry.Registry` (the J/S-20.T2 package this ticket's Reconcile
consumes) over its own real, file-backed SQLite database, seeded through
that package's own `UpsertProvider`/`UpsertLane` -- never a hand-built
fake registry or a stubbed reader.

## Fixture provenance (P1-E40-W9-S77-T3)

- `source_ladder_merge.golden.json` -- seven hand-authored cases covering
  every precedence pair (provider-status/cli-observation/user-estimate/
  unknown) plus the observed_at and confidence tie-breaks, transcribed
  from R-21.26's own ladder ordering, never captured from `Merge`'s own
  output. `TestSourceLadderMerge` (source_ladder_test.go) asserts `Merge`
  against every row.

- `window_map.golden.json` -- the nine-row R-21.116 mandatory
  dimension-to-window map, transcribed verbatim from the ruling text
  (rpm/tpm -> rolling 60s, rpd -> day, session_5h/window_5h -> 5h,
  weekly_shared/weekly_model_fraction/weekly -> week, monthly -> month).
  `TestWindowForEveryDimension` (window_map_test.go) asserts `WindowFor`
  against every row.

- `availability.golden.json` -- four hand-computed cases (no reservations,
  held+committed both counting, a reservation on a different scope or
  dimension being ignored, and an overcommitted bucket going negative),
  computed by hand from R-21.114's own formula
  (`capacity_observed - committed_since_observation - sum(estimates)`),
  never captured from `Available`'s own output. `TestAvailableDerivedFromReservations`
  (availability_test.go) asserts `Available` against every row.

- `fixture_quota_snapshot_rpc.json` -- captured by `rpc_quota_test.go`'s
  `TestQuotaSnapshotRPCOverSocket`, which dispatches a real
  `fleet.quota.snapshot` JSON-RPC 2.0 request through a real
  `*rpc.Registry.Dispatch` call (the same production dispatch path the
  daemon's POST /rpc unix-socket handler uses) against a real, file-backed
  SQLite `QuotaStore`/`Store` pair, and writes the request/response pair
  to this file on every test run. The clock and every seeded row are
  fixed, so the content is deterministic across runs -- an accidental
  change to the wire shape or the confidence-minimum computation shows up
  as a git diff against this tracked file. See
  `internal/fleet/capacity/testdata/README.md`'s identical
  `fixture_snapshot_rpc.json` entry for the established precedent this
  follows, including the CONTRACT DEVIATION on why the in-package proof
  stops at `Registry.Dispatch` rather than a live unix socket (a full
  socket round trip is the daemon composition root's own concern -- see
  this ticket's journal and `internal/daemon/quota_rpc_test.go`).
