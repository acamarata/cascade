# CI result routing

Each terminal CI result is routed once, to the delegation that asked for it.
The daemon's "ci-fanout" registration (`cmd/cascade/wire_ci_fanout.go`,
phase supervised, Order 23) calls `ci.RegisterCIFanout` once on the daemon's
one CI dispatcher. That adds exactly one `OnTerminal` callback. It runs after
the "notify" registration (Order 5) builds the one `notify.Service` and the
"ci" registration (Order 20) builds the dispatcher.

## Projection

For each terminal sub-job the callback reads the `ci_run` and `ci_job` rows
of the event's run key and builds a `CIResultNotice`:

| Field | Source |
| --- | --- |
| `JobID`, `RepoID` | the event |
| `CommitSHA`, `StartedAt`, `FinishedAt` | the `ci_run` / `ci_job` rows |
| `Status` | the run conclusion (table below) |
| `LogRef` | the `ci_run` row the log hangs off |
| `DelegationID`, `OriginPrincipal`, `OriginSession` | the job's newest delegation record |
| `OriginScope` | the scope the origin reports, when it knows one |

| Conclusion | Status |
| --- | --- |
| `success` | `accepted` |
| `failure` | `rejected` |
| `cancelled` | `cancelled` |
| anything else | `failed` |

A missing `ci_run` row is `KindNotFound`. The daemon's origin reads the
newest `jobs_delegation` row whose `job_id` is the event's job. No new table
is added.

## Routing

The notice is delivered through the daemon's `notify.Service`. `Source` and
`Kind` are both `ci.result`, and `Payload` is the notice as JSON.

1. **Addressed to the origin principal.** When the delegation record names
   an origin principal, the item is `Class` addressed, `TargetPrincipal` =
   `OriginPrincipal` and `CorrelationID` = the delegation record ID.
   `TargetSession` is never set alongside it. Only that principal can read
   the item back through `ReadAddressed`. Any other caller gets
   `KindPermissionDenied`.
2. **Scoped.** When no principal is known but a scope is, the item is
   `Class` scoped with `TargetScope` = that scope, and a warning is logged.
3. **CI attention item.** When there is no origin at all, the result is
   filed as a CI attention item through the daemon's attention store, and a
   warning is logged.

A CI result never uses `ClassGlobalCritical`.

## Errors

`RouteToInbox` returns `Deliver` and `Push` failures unchanged. The
`OnTerminal` callback has no error return, so the fanout logs projection and
delivery failures through `ci.Warn` and the dispatcher keeps running.

## Once only

The checkpoint stream writes `via_stream` results, and the fanout delivers
them. Under the routing contract, the completion path (`handleRunCompleted`
in `internal/ci/attention_subscribe.go`) skips `via_stream` payloads, so the
stream and the fanout never both notify for the same result.
