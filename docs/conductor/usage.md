# Conductor usage and cost attribution

`internal/conductor/usage.go` and `usage_migration.go` implement R-16.52's
two-store usage/cost accounting for every terminal `conductor.Execute`
outcome (P1-E11-W3-S23-T4).

## Two stores, both named (R-16.52)

Every terminal outcome writes to two independent stores, sourced from the
same dispatch data:

1. **Per-job row** (this ticket owns it): one `UsageRecord` inserted into
   the `jobs` domain's `jobs_usage` table, keyed by `job_id`. Written once,
   via `WriteUsageRecord`; never updated by this ticket.
2. **Bucketed aggregate** (J/S-20.T4 owns it): `internal/providers/usage`'s
   `(*Manager).IncrementUsage` call, keyed by
   `(provider_name, lane_name, model_name, day_bucket)`.

There is no shared `AccountingStore` interface and no `RecordUsage`
method spanning both — K (conductor) and J (providers/usage) each own
their own store and their own write path. `UsageAggregator` (usage.go) is
a narrow, locally-declared interface matching `IncrementUsage`'s real
signature only, for testability, not a shared seam.

## Fields

`UsageRecord`: `JobID`, `LaneID`, `TaskClass`, `TokensIn`, `TokensOut`,
`CostMicroUSD`, `WallMS`, `OutcomeClass` (`"unknown"` by default; only
`UpdateOutcomeClass` ever changes it), `Attempt` (1, or 2 only when the
single allowed capability-denied failover re-dispatched), and
`RequestingEntity`.

## Requesting-entity resolution

Priority order, evaluated per dispatch from context values:

1. `plugin_id` present → `"plugin:<id>"`
2. else `session_id` present → `"session:<id>"`
3. else `"standalone"`

No exported context setter ships with this ticket: neither producer (the
plugin host, O/S-31.*, or the session layer, L/S-24.*) exists yet. The
ticket that wires either producer adds the matching `With*` setter in
`usage.go` against the same unexported key types, when it lands.

## Fail-closed accounting semantics per store

Each write is attempted and reported independently. Either failing:

- is logged at `warn` level, naming which write failed;
- publishes a `usage_record_failed` event on the `conductor.usage` bus
  namespace (if an `EventBridge` is installed);
- **never** surfaces to `Execute`'s caller and **never** blocks the other
  write.

## Single-dispatch-only hook (R-21.214)

The attribution hook (`attributeUsage`) is called only from `Execute`'s
two post-dispatch terminal branches (success, provider error/
cancellation) — never from the fan-out parent branch. `fanout.go`'s
`FanOut` primitive dispatches every leg through `Execute` itself (the
`exec` function `ExecuteFanOut` passes in), so each leg gets its own
`UsageRecord` row and its own `IncrementUsage` call, keyed by that leg's
own `JobID`. The fan-out parent (`assembleFanOutParent`) never calls
`Execute` again for itself — it only sums the legs' own `Usage` and mints
a fresh parent `JobID` — so for `fan_out = n`, exactly `n` rows and `n`
`IncrementUsage` calls exist, and the parent's own `JobID` owns none of
them.

## job_id correlation

`job_id` is the single correlation key across:

- this ticket's `jobs_usage` row (written once, at the terminal outcome);
- S-23.T3's per-job SSE terminal event (`conductor` bus namespace,
  `job:<id>` event kind);
- AE/S-64.T1's later `UpdateOutcomeClass(ctx, jobID, outcomeClass)` call,
  which updates only the `outcome_class` column of the existing row (a
  typed not-found error, writing nothing, on an unknown `job_id`).

## Partial counts on a cancelled dispatch

If a dispatch is cancelled (`job.cancel`, or the caller's own context)
before the provider ever returns a usage figure, the terminal branch that
observes the cancellation still calls `attributeUsage` with whatever
`Usage` the provider response carried — partial `tokens_in` (from
whatever was consumed so far) and `tokens_out` of `0` when nothing was
generated yet. No special-case branch exists for this: it is the ordinary
provider-error branch, since a mid-flight cancellation surfaces as the
provider call returning the context's own error.

## Where the numbers come from, and how to tell they are being counted

The two stores above are populated by the daemon at startup, through
`daemon.ConductorAccounting`, which carries all three write seams the
executor exposes:

| Seam | Target |
|---|---|
| `SetUsageStore` | one `jobs_usage` row per dispatch, in `~/.cascade/data/cascade.db` |
| `SetUsageAggregator` | the per-provider counters in `~/.cascade/data/provider-usage.db` — the SAME file `cascade provider usage` reads |
| `SetCostEstimator` | the provider registry's rate card |

**All three or none.** Wiring the two writers and leaving the estimator
nil would record every dispatch at a cost of zero — a wrong number, which
an operator believes, rather than an absent one, which prompts a question.

**`cascade status` says whether it is on:**

```
subsystem:conductor.executor  running (executor constructed; usage accounting wired)
```

The other two values are `usage accounting partially wired (some dispatch
facts are not counted)` and `usage accounting NOT wired (dispatches are not
counted)`. That line exists because this subsystem's failure mode is
silence: for the whole of Waves 3 and 4 the three seams had no production
caller, `attributeUsage` wrote through nil hooks, and `cascade provider
usage` reported an empty table on a machine that had been dispatching all
week — no error, no warning, no degraded subsystem (R-14.283).

## What a zero cost means

The cost estimator prices a dispatch from the registry's rate card. A
provider whose `CostRecord` is nil — the registry's explicit "prices were
never fetched" — prices at **zero**, and a `jobs_usage` row has nowhere to
carry the difference between "this was free" and "cascade does not know".

So the distinction lives where an operator reads it, in `cascade provider
list`'s `cost` column:

| Column value | Meaning |
|---|---|
| `unknown` | no rate card has ever been fetched for this provider |
| `no per-model prices` | a rate card exists and names no models — the normal state of a flat-rate subscription |
| `N model(s) priced` | N models have a per-token price |

A spend report that comes out at zero is read against that column.
