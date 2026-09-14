# Conductor routing matrix

`internal/conductor/router.go` (K/S-22.T2) is the Conductor's sole
lane-selection entry point: `Router.Select`/`SelectExplain` apply five
sequential filters over one immutable metadata snapshot taken at the start
of each call (R-21.213). Each filter can only narrow the candidate set it
receives; none re-reads the registry, the eviction state, or the quota
policy mid-pass. This page documents the filter pipeline, the
sensitivity-tier enforcement rules, the fail-closed contract, and the
explainable-reason (`ReasonFlags`) format the acceptance golden matrix
(P1-E11-W3-S23-T5, `internal/conductor/routing_matrix_test.go`) proves
against the real implementation.

## The five filters, in order

| # | Filter | File | Narrows on | Fail-closed error |
|---|---|---|---|---|
| 1 | Capability | `filters_capability.go` | `req.RequiredCapabilities` against each lane's cached `Capabilities` | `ErrNoCapableProvider` |
| 2 | Sensitivity | `filters_capability.go` | the resolved `SensitivityTier` | `ErrNoLane` (local-only leg only) |
| 3 | Health | `filters_dispatch.go` | `ProviderInfo.HealthStatus` | `ErrAllProvidersEvicted` |
| 4 | Quota/spill | `filters_dispatch.go` | `QuotaSpiller.NextLane`'s ordering | (propagates `QuotaSpiller`'s own error, e.g. `ErrAllLanesExhausted`) |
| 5 | Cost | `filters_dispatch.go` | none - builds the final `Selection` from FILTER 4's single survivor | none |

`Select` is a thin wrapper over `SelectExplain` that discards the second
return value; both run the identical evaluation of one `routeSnapshot`, so
the `ReasonFlags` an explanation carries can never drift from the decision
that was actually made (R-14.85).

### FILTER 1 - capability

A lane whose relevant capability dimension is `CapabilityUnknown` (never
probed) is excluded whenever that dimension is required - never treated as
"unknown, so allow" (R-21.213). This is distinguishable from an explicit
`CapabilityUnsupported` denial in the emitted flags: an unprobed exclusion
adds a `capability:N-unprobed-excluded` flag before the terminal
`capability:N-matched` flag; a plain denial emits only the terminal flag.

### FILTER 2 - sensitivity

An unset or invalid `Sensitivity` value resolves to `SensitivityRestricted`
before this filter runs (see `resolve.go`'s `ResolveSensitivity`, the same
function `execute.go`'s `authorize` calls). `local-only` retains exactly
the lanes whose computed locality is local (a loopback or unix-socket
`BaseURL`) and removes every remote-locality lane; an emptied local-only
set returns `ErrNoLane`. The `restricted`/`internal`/`public` legs are a
recorded CONTRACT DEVIATION: the real `ProviderRegistryReader` carries no
node-trust-tier vocabulary, so those three legs remove nothing and record
the fact via a `sensitivity:<tier>:0-filtered` flag rather than silently
widening the tier.

### FILTER 3 - health

Removes any lane whose owning provider's `HealthStatus` is not `""` or
`"healthy"`. An emptied set returns `ErrAllProvidersEvicted`.

### FILTER 4 - quota/spill

Calls `QuotaSpiller.NextLane` over the survivors FILTERS 1-3 already
produced, with every already-filtered lane added to the exclude set so
spill can never reintroduce a lane an earlier filter removed. The picked
lane's flag names whether it came from a pool (`quota:pool-<pool>:lane-<x>`)
or priority ordering (`quota:priority:lane-<x>`).

### FILTER 5 - cost

Builds the final `Selection` from FILTER 4's single surviving candidate.
**Cost-tie is not a producible outcome**: `filterCost`'s own signature
takes exactly one `laneCandidate`, never a slice, because `NextLane`
already resolved the set to one lane by the time this filter runs. It
always emits the literal `cost:cheapest-selected` flag, unconditionally -
this is proven, not assumed, by
`TestRoutingMatrix_CostTieUnreachable` and documented in
`internal/conductor/testdata/routing_matrix/README.md`.

## Fail-closed contract

- Unknown or empty `task_class` -> `cascade.ErrInvalidRequest`
  (`ResolveTaskClass`, terminal deny, R-21.208 - never an approval or
  elevation path).
- Unset or unresolvable `Sensitivity` -> resolves to `SensitivityRestricted`
  (`ResolveSensitivity`), never a permissive default.
- No lane satisfies the required capabilities -> `ErrNoCapableProvider`.
- Every capability-matched lane is evicted or unhealthy ->
  `ErrAllProvidersEvicted`.

## Explainable-reason (`ReasonFlags`) format

`provider.Selection.ReasonFlags` carries one flag per filter, in filter
order, e.g.:

```
["capability:1-matched", "sensitivity:restricted:0-filtered",
 "health:0-evicted", "quota:priority:lane-lane-a", "cost:cheapest-selected"]
```

The full acceptance matrix - every (task_class, sensitivity_tier) pair
from the §5.16 nine-class taxonomy crossed with the four sensitivity
tiers, plus one representative fixture per filter-stage outcome - lives in
`internal/conductor/testdata/routing_matrix/goldens.yaml`, with provenance
in that directory's `README.md`.
