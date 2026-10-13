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
| 2 | Sensitivity | `filters_capability.go` | the resolved `SensitivityTier` | `ErrNoLane` (local-only tier), `ErrSensitivityViolation` (every other tier) |
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
(`resolve.go`'s `ResolveSensitivity`, the same function `execute.go`'s
`authorize` calls). Every candidate lane is then decided by ONE table,
`SensitivityAllows` in `lane_table.go`. The thread-privacy filter (FILTER 0,
`privacy.go`) reads the same table, so the two gates cannot disagree.

| tier \ lane | controller-local | external-api | unresolved |
|---|---|---|---|
| local-only | allow | refuse | refuse |
| restricted | allow | allow only if permitted | refuse |
| internal | allow | allow | refuse |
| public | allow | allow | allow |

A lane is controller-local when its `BaseURL` is a host-less `unix://` socket,
`localhost`, `::1` or a dotted-quad address in `127.0.0.0/8`. Anything else
with a host is external-api (so `127.evil.com`, `localhost.evil.com` and a
`unix://` URL that names a host are external). An empty or unparseable
`BaseURL` is unresolved. A tier outside the four reads as restricted.

**Restricted permits.** Restricted content may use an external lane only when
the operator names that lane in the guarded config key
`[conductor] restricted_external_lanes` (a list of exact lane names, default
empty). A wildcard, a non-string entry or a non-list value refuses the config
with `invalid_input`; a permit never admits an unresolved lane. The list is
read once when the daemon builds the conductor, so a change applies at the next
start. Adding a lane loosens the policy, so the key belongs to the guarded
`[conductor]` section. Every default request is restricted, so until the
operator lists a cloud lane the conductor routes only to controller-local
lanes.

**Refusal.** `local-only` keeps its R-21.228 contract: an emptied set returns
`ErrNoLane` and the flag is the count alone. For every other tier each removed
lane adds a flag `sensitivity:<tier>:removed:<lane>(<lanetype>)` (sorted by
lane name), and an emptied set returns `ErrSensitivityViolation` (kind
`policy_denied`) naming the tier, the first removed lane and its type, the
count and the `conductor.restricted_external_lanes` key.

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
