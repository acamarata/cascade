# testdata provenance

## v1-goldens/quota.json

Hand-built representative sample (2 accounts + 1 pool) of v1's
`~/.cascade/accounts/quota.json` shape, per R-16.35's SPEC-SALVAGE
citation. Field set copied verbatim from
`../../../../cascade-v1/src/widget/macos/CascadeApp/UsageCache.swift`'s
`AccountEntry`/`UsageBlock`/`QuotaSlot` Codable types: `account`,
`provider`, `email`, `quota_opaque`, and per-window `utilization`,
`resets_at`, `resets_in`, `status` under `five_hour`/`seven_day`/
`seven_day_opus`. The `acc2` entry deliberately carries both `seven_day`
and `seven_day_opus` to exercise the v1 fallback rule
`UsageRow.swift`'s `weekUtil` documents (`seven_day_opus?.utilization ??
seven_day?.utilization` — the opus-specific weekly window wins when
present). `gfp-pool` represents the pool case with `quota_opaque: true`
and no `email`.

`internal/fleet/capacity/widget_test.go`'s `TestWidgetComposeV1GoldenParity`
decodes this file directly (relative path `../../daemon/testdata/
v1-goldens/quota.json` from that package) and asserts the v2 row-shape
mapping this ticket keeps: `utilization` -> `utilization_pct`,
`resets_in`/`resets_at` -> `resets_in`, `status` -> `state`, and the
opus-fallback rule above. `.github/wiki/Accounts-and-Models.md`'s
"Menu-bar / widget readout" section is this ticket's provenance for the
never-fabricate ("-") rule Compose/Redact also apply.

## fixture_status_widget_rpc.json

Captured by `status_widget_test.go`'s `TestStatusWidgetRPC`, which
dispatches a real `status.widget` JSON-RPC 2.0 request through a real
`*rpc.Registry.Dispatch` call (the same production dispatch path the
daemon's unix-socket HTTP handler uses — `internal/rpc/handler.go`'s
`Handler.ServeHTTP` calls exactly this method) and writes the request/
response pair to this file on every test run. The clock is a fixed fake,
so the content is deterministic across runs.

CONTRACT DEVIATION (recorded, not papered over): the ticket's task 7 asks
for a fixture captured "against a real in-process daemon socket". This
ticket's own `checks` list runs `TestStatusWidgetRPC` in the default
(non-`integration`-tagged) build lane, which forbids importing `net`
(AGENT-BRIEF.md's REPO-WIDE GATES: "the default unit lane forbids
importing net; real-socket tests go behind the `integration` tag"), so a
literal unix-socket round trip cannot live in this test. This mirrors
`internal/fleet/capacity/testdata/README.md`'s identical, already-landed
deviation for `fixture_snapshot_rpc.json` (S-63.T1) and
`internal/conversation/adapter_test.go`'s own doc comment for the same
reason: `Registry.Dispatch` is the real production entry point either
way, so this still satisfies Art.2's "real counterpart, not a mock"
requirement, just not the full unix-socket round trip in this particular
test file.
