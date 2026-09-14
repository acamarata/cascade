# Routing-matrix golden provenance (P1-E11-W3-S23-T5)

## Spec source

There is no v1 or external real-run counterpart for this router: it is new
v2 logic (K/S-22.T2), not a port. Per the ticket's own spec_refs, the
provenance is "K/S-22 interface + §5.16 normative table" - concretely:

- The task-class axis (9 rows) is `TaskClasses()` in
  `internal/conductor/task_classes.go`, itself a verbatim transcription of
  06-FORGE-SPEC.md §5.16.
- The sensitivity-tier axis (4 rows) is the declared member set of
  `provider.SensitivityTier` (`pkg/provider/model.go`).
- Every `want_flags`/`want_lane_id`/`want_err` value in `goldens.yaml` was
  computed by hand, reading the five filter functions' exact format
  strings and branch conditions in `router.go`, `filters_capability.go`
  and `filters_dispatch.go`, BEFORE the test that asserts them was run for
  the first time. This is a stated, reproducible derivation (each format
  string is quoted below), not a capture of the implementation's own
  output.

## Fixture design for the 36-pair matrix

Every `pairs` row uses the same one-lane fixture: a single healthy,
loopback-hosted lane (`lane-a`/`prov-a`, `BaseURL: http://127.0.0.1:8080`,
`HealthStatus: healthy`, no `PoolMembership`) with no required
capabilities. This lane survives every filter regardless of task class or
sensitivity tier, so the matrix isolates exactly the two axes the ticket
asks it to cover (task_class × sensitivity_tier) without also varying the
provider-side fixture per row. Filter-stage-specific behavior (a lane
being removed, denied, or evicted) is covered separately by the
`filter_stages` rows below, each of which deliberately varies exactly one
filter's input.

Per-row flag derivation (all four sensitivity tiers, single local lane,
zero required capabilities):

- `capability:1-matched` - `filterCapability` (filters_capability.go:52):
  `anyCapabilityRequired` is false (no capability set), so the lane is
  appended unconditionally and `capability:%d-matched` reports len(out)=1.
  No unprobed flag - `unprobed` stays 0.
- `sensitivity:<tier>:0-filtered` - for `local-only`, `filterLocalOnly`
  (filters_capability.go:131) removes zero lanes because
  `computedLocalityIsLocal` (line 144) returns true for a
  `127.0.0.1`-hosted BaseURL, so `removed` stays 0. For
  `restricted`/`internal`/`public`, `filterSensitivity`'s own default leg
  (filters_capability.go:107/110) always appends
  `sensitivity:%s:0-filtered` unconditionally (a CONTRACT DEVIATION already
  recorded in that file's header: the real registry carries no node-trust
  vocabulary for those three tiers to filter against).
- `health:0-evicted` - `filterHealth` (filters_dispatch.go:43): the lane's
  `HealthStatus` is `"healthy"`, so `evicted` stays 0.
- `quota:priority:lane-lane-a` - `quotaFlag` (filters_dispatch.go:88-92):
  `PoolMembership` is empty, so the `priority` branch fires with the
  picked lane name.
- `cost:cheapest-selected` - `filterCost` (filters_dispatch.go:101) appends
  this literal string unconditionally, every call, no branch.

## Filter-stage fixture rows

Each `filter_stages` row deliberately varies exactly one filter's input
against the same one-lane base fixture, to produce one representative
golden per K/S-22.T2 filter-stage outcome:

| name | filter varied | expected outcome |
|---|---|---|
| capability-matched | FILTER 1 | lane retained, `capability:1-matched` |
| capability-denied | FILTER 1 | `CapabilityUnsupported` -> `ErrNoCapableProvider`, `capability:0-matched` only (no unprobed flag: an explicitly-denied dimension is not an unprobed one) |
| capability-unprobed-excluded | FILTER 1 | `CapabilityUnknown` -> `ErrNoCapableProvider`, `capability:1-unprobed-excluded` then `capability:0-matched` |
| sensitivity-permitted-local | FILTER 2 | `local-only` tier, loopback lane -> retained |
| sensitivity-filtered-local | FILTER 2 | `local-only` tier, a non-loopback `BaseURL` -> removed, `ErrNoLane` (filterLocalOnly's own contract: an emptied local-only set returns `ErrNoLane`, never `ErrSensitivityViolation` - see filters_capability.go:117-120) |
| health-clean | FILTER 3 | `HealthStatus: healthy` -> retained |
| health-evicted | FILTER 3 | `HealthStatus: evicted` -> `ErrAllProvidersEvicted` |
| quota-pool-selected | FILTER 4 | `PoolMembership: pool-x` -> `quota:pool-pool-x:lane-lane-a` |
| quota-priority-selected | FILTER 4 | empty `PoolMembership` -> `quota:priority:lane-lane-a` |
| cost-cheapest-selected | FILTER 5 | always `cost:cheapest-selected` |

## cost-tie is not representable (the finding, not a gap in this fixture set)

T5's own task list asks for a "cost-cheapest-selected and cost-tie" fixture
pair. `filterCost`'s signature is
`filterCost(cand laneCandidate, classes []TaskClassRow, req provider.ModelRequest, flags []string)`
- it takes exactly ONE `laneCandidate`, never a slice. By the time FILTER 5
runs, FILTER 4 (`filterQuota`) has already collapsed the candidate set to
exactly one lane, because `QuotaSpiller.NextLane` returns exactly one
`LaneID` by its own interface contract (router.go:49-51), never a
reordered or tied set. There is no legal Go call into the real Select
pipeline, and no legal call to `filterCost` itself, that can ever present
two candidates for a cost comparison - this is a structural fact about the
function's own type signature, not a runtime observation. `filterCost`
also has no branch: it appends the literal string `"cost:cheapest-
selected"` unconditionally on every call (filters_dispatch.go:101).

`TestRoutingMatrix_CostTieUnreachable`
(`internal/conductor/routing_matrix_edge_test.go`) pins this: it calls
`filterCost` twice with two different single-lane candidates and asserts
both produce the identical literal flag, proving the cost stage's output
never varies by which lane survives - there is no observable "tie"
outcome to assert, because none can occur. This finding is not resolved by
inventing a fixture; the disagreement between the ticket's literal task
text and the real Select pipeline is the deliverable.

## Regenerating this file

`goldens.yaml`'s `pairs` list was generated by a one-off script
(`/tmp/gen_goldens.py`, not checked in - scratch only) that emits the
9 task classes x 4 sensitivity tiers cross product with the derived flags
above. To regenerate: rebuild the same script from the derivation rules
in this README (task classes from `TaskClasses()`, tiers from
`provider.SensitivityTier`'s declared members, flags from the format
strings cited above) - never from a test run's captured output. The
`filter_stages` list is fixed by hand; regenerate it by copying the table
above.

## Determinism

`TestRoutingMatrixDeterminism` (`routing_matrix_test.go`) calls
`Router.SelectExplain` twice, in the same process, with byte-identical
inputs (the same request value and a freshly-built-but-equal registry
double per call), for every `pairs` row, and asserts the two returned
`(Selection, flags)` pairs are `reflect.DeepEqual`. This proves
determinism by direct comparison of two real calls, not by asserting a
sorted order the code itself produces. The ticket's own
`go test -race -count=2 -run TestRoutingMatrix` check additionally
re-runs the whole matrix test a second time at the process level under
the race detector, so a hidden shared-state or ordering dependency across
runs would also surface there.
