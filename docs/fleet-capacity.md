# Fleet capacity

How Cascade decides whether a dispatch may run, and how it keeps that
decision honest across crashes and restarts.

## Reservation ledger

Every dispatch that spends provider quota holds one row in
`jobs_reservation`. The row is the ledger: availability is derived from it,
leases and worktrees are recorded on it, and recovery reads it. There is no
second reservation table. Code: `internal/fleet/economics/reserve*.go`.

### Record

| Group | Fields |
|---|---|
| Identity | `id`, `execution_id` (unique), `job_id`, `project_id`, `lane_id` |
| Quota | `domain_id`, `scope_id` (the domain's limit scope), `kind`, `estimate`, `actual`, `actual_source`, `base_price`, `price_table_version`, `scarce_units` |
| Resources | `repo_id`, `scope_globs`, `lease_ids`, `worktree_id`, `permit_id` |
| Ledger | `steps` (`{step, idempotency_key, handle, state}`), `state` |
| Liveness | `owner_epoch`, `heartbeat_at`, `expires_at`, `created` |
| Placement | `node_id`, `selected_tier`, `sensitivity`, `decision_id` (written by Bind) |

`repo_id` and `scope_globs` are stored so recovery and resume never depend
on the request that created the row.

### States and kinds

States: `held`, `parked`, `committed`, `released`, `rolled_back`. Legal
moves: held to parked and back, held to committed, committed to parked,
held or parked to `released` or `rolled_back`, and committed to `released`
only. `released` and `rolled_back` are terminal. Every state change goes
through the store's transition, which is compare-and-set on the stored state.
A pure state move (park, unpark, commit) writes the state column and nothing
else. A progress write (step ledger, handles, accounting, and the teardown
that ends in a terminal state) never writes the placement or the liveness
columns. So a `Bind` or a claim that lands after a writer read the row is
never erased by that writer.

Kinds: `interactive` (the default; takes leases and a worktree when it has
scope globs) and `batch` (quota only, no globs, a 24 hour expiry).

### Order

1. Insert the held row, with a pending `quota` step.
2. Admit it (see Dimensions and Project share). Insert and admission run
   under the Reserver's mutex, so concurrent requests on one daemon never
   read the same outstanding total.
3. If the request has scope globs: record a pending `leases` step, acquire
   the leases in `repo_id`, record them; record a pending `worktree` step,
   allocate the worktree on the first lease, record it.

The permit is not part of the reservation. `WithPermit` takes it right
before each adapter call and releases it when the call returns. A parked
reservation holds none.

### Reverse rollback

Any failure tears down what was acquired in reverse order: worktree first,
then leases. Every compensation is attempted even if an earlier one fails;
failures are joined under `ErrReservationRollback` together with the
original cause. The row is never deleted, only moved to `rolled_back`.
Compensation ignores the caller's cancellation, so a cancelled request
cannot strand a lease.

A compensation that fails is returned, never retried. The terminal row
keeps the handle that could not be released (a worktree id or lease ids)
for an operator; `Sweep` and `ExpireStale` never revisit terminal rows.

### Step ledger and recovery

Each step is written as `pending` before its call and `acquired` after it.
`RecoverSteps` resolves a pending step by asking the subsystem with what the
row stores:

- leases: `FindLeases(repo_id, job_id, scope_globs)`;
- worktree: `FindWorktree(first lease id)`.

A found handle is recorded so the teardown that follows releases it. Nothing
found marks the step `not_run`. A lookup error leaves the row exactly as it
was and is returned; it is never read as "nothing happened".

### Bind

`Bind(id, placement, fn)` opens one transaction on the ledger database,
checks the row is still held, writes the placement, runs the caller's `fn`
on the same transaction (its execution and scheduler-decision rows), and
commits. If the placement write, `fn` or the commit fails, the transaction
is rolled back and the reservation is rolled back through the reverse
teardown.

### Liveness, sweep fencing and expiry

- The Reserver keeps the ids it holds in memory. `Heartbeat` renews each
  one's `heartbeat_at` and its leases. A lease refused as a conflict
  (fenced, or past its ttl plus grace) means the reservation is lost: it is
  torn down, dropped from memory and reported once through the attention
  seam with the reason `lost_lease`. A renewal that fails for any other
  reason is neither renewed nor lost: the row stays held and the next tick
  retries it.
- `Sweep` runs at daemon start. If another daemon is live on the same home
  it returns `ErrConcurrentDaemon` and writes nothing. Otherwise every held
  or parked row of another epoch is recovered and rolled back at once (its
  owner is dead), every committed row of another epoch is adopted (epoch
  rewritten, heartbeat refreshed, not held in memory), and expired batch
  rows are retired.
- `ExpireStale` runs every heartbeat interval. It retires rows that are not
  held in memory and whose heartbeat is older than three intervals. It never
  touches the Reserver's own rows. `Adopt` lets re-admission claim an
  adopted row before that happens. Once `ExpireStale` has started retiring
  a row, `Adopt` refuses it: a claim either lands first (the row is skipped)
  or is refused, never both. Before any recovery or teardown, `ExpireStale`
  fences the row in the database: `owner_epoch` moves to its own epoch only
  if epoch, heartbeat and state are still what it listed. A row that
  another process adopted, renewed or claimed since then is skipped, and
  that process can no longer adopt a row once it is fenced.

Cross-process serialization is refused, not provided: one daemon owns the
ledger, and a second one is stopped at `Sweep`.

### Dimensions

Dimensions are topology's closed names per domain kind. The ledger adds no
names of its own. `ReservationUnits` maps an estimate onto each one:

| Domain kind | Dimension | Units |
|---|---|---|
| `api_project` | `rpm`, `rpd` | requests |
| `api_project` | `tpm` | tokens in + tokens out |
| `subscription_window` | `session_5h`, `weekly_shared`, `weekly_model_fraction`, `monthly` | requests |
| `shared_pool` | `window_5h`, `weekly`, `monthly` | requests |

For every bucket of the domain, `topology.Reservable` must hold, and unless
the bucket is undiscovered (no limit and no observed capacity), the
request must fit `topology.Available`: observed capacity (or the limit when
none was observed), minus committed usage since the observation, minus the
estimates of every held, parked and committed reservation on the same limit
scope. The scope is summed across domains, so two domains that share an
account scope see each other. Refusals are fail-closed and name the cause:
an unknown domain kind, a domain with no buckets, a dimension of the kind
with no bucket, a bucket on another scope, a bucket without a units row, or
a limit of 0. Buckets are written only by
`topology.QuotaStore.UpsertBucket`, which refuses a dimension outside the
stored domain kind.

### Project share

For the domain kind's barrier bucket (`rpd`, `weekly_shared` or `weekly`),
each project may hold at most

    (remaining fraction - account reserve) / max(1, active projects) x capacity

units. The reserve is the account role's preserved weekly reserve and the
count comes from `ActiveProjectCount`. A project over its share gets
`ErrProjectShareExceeded`. A missing barrier bucket refuses
`ErrQuotaUnavailable`; only an undiscovered barrier skips the rule. The check
runs in the same critical section as the insert.

### Actuals and scarce units

`Reconcile(id, actual, source)` stores a reported usage verbatim. With no
report it keeps the estimate and marks the source `estimated`, never zero.
`scarce_units` is the stored base price times the actual requests. Pressure,
mode and reserve do not enter it.

### Rate-limit cascade

`CascadeRateLimited(scope)` parks every held reservation on a rate-limited
scope. Committed reservations run on. Parked reservations keep their quota
and leases but hold no permit. The count it returns is the parks that
landed. A reservation that `Reserve` is still admitting or acquiring is
parked when that `Reserve` finishes, so its acquisition is never cut off
half way. It is not in the count, and if that acquisition fails the row
is rolled back instead. `Reserve` clears its in-flight mark and its
admission lock by defer; if a seam panics, the row is recovered from its
step ledger and rolled back before the panic continues.

### Resume

`ReacquireForResume(id)` revalidates the stored leases after a restart. If
all are valid they are kept. Otherwise the stored leases that are still
valid are released and the whole stored scope is re-acquired in one call
with the stored repo, job and globs. If that fails, the reservation is
rolled back.

## Fleet.Lease

`Fleet.Lease` is the one in-process door from a request to a reservation.
It is not an RPC. Code: `internal/fleet/capacity/fleet.go`, `resolve.go`,
`lease.go`. Build a `Fleet` only with `capacity.NewFleet(FleetDeps)`; every
field is required except `Floors`, and a missing one is `KindInvalidInput`
naming it.

### Request and dispatch reference

`Lease(ctx, req, d, attempt)` takes a `ResourceRequest` (what the work
needs), a `DispatchRef` (what it is) and the attempt number (1 or more).

| `DispatchRef` field | Rule |
|---|---|
| `ProjectID` | required |
| `Requests` | at least 1; `HydratedTokens` at least 0 |
| `JobID` | set: the execution row is written; empty: no execution row |
| `RepoID`, `ScopeGlobs` | globs need both a `JobID` and a `RepoID`; the reservation leases them; the teardown removes the worktree, then releases them |
| `AgentOnly` | restrict to `agent:` lanes (otherwise non-agent lanes) |
| `Placement` | nil runs on the controller; otherwise node capabilities only |

`PlacementNeed` carries no sensitivity. The placement requirement's tier is
derived from the request: `JoinSensitivity(req.Sensitivity,
TierOrLocalOnly(req.DataClass))`. The caller never supplies it.

The data-class floor comes from the leased job's own data class. A request
that leaves `DataClass` nil takes the job's class (the mapping `Lease` owns);
one that sets it below the job's class is refused. A request with no job has
nothing to derive from, so its data class reads local-only and it can run only
on controller-local lanes.

### Order

1. Validate the dispatch reference and the request (`ValidateResourceRequest`).
2. Read the snapshot.
3. Allowed lanes: snapshot lanes with `Posture.ProgrammaticEntitlement`,
   agent lanes only when `AgentOnly`. None: `ErrNoAllowedLanes`.
4. Sensitivity filter: `conductor.SensitivityAllows` over the joined tier.
   This runs first and cannot be removed.
5. `FleetDeps.Floors`, in order, over the lanes the filter kept.
6. The `Selector` chooses tier and lane from what is left.
7. Node placement (only when `Placement` is set).
8. `DomainOf(lane)`; a lane with no quota domain refuses. Unknown capacity is
   never unlimited.
9. `Reserve` (execution id minted), then `Bind` in one transaction: the job's
   data class is read again inside it (a class raised since step 1 refuses
   with `ErrDataClassDenied`), then the placement, the execution row (when
   there is a job) and the scheduler decision through the `DecisionRecorder`.

Nothing is written before `Reserve`, and every refusal before it writes zero
rows. `Lease` never sets `ReserveRequest.ScopeID`; the Reserver derives the
limit scope. The Selector and the floors get a private copy of the snapshot
and of the eligible lanes. `Lease` keeps its own record of the eligible lanes
and their tiers and refuses a Selection whose lane was not eligible before the
Selector ran, or whose tier is not that lane's tier.

### Errors

| Error | Kind | When |
|---|---|---|
| `ErrNoCapableTier` (wraps `ErrFleetExhausted`) | unavailable | no schedulable tier, or the floors removed every lane |
| `ErrTier0Reserved` | unavailable | tier-0 is schedulable (after backoff), `reserve_tier0` is set and no jump fired |
| `ErrSensitivityConstraint` | policy denied | local-only work with no controller lane or node, or restricted work with no trusted node |
| `ErrDataClassDenied` | policy denied | the data class forbids every remaining lane |
| `ErrReservationFailed` | unavailable | reserve or bind failed; the cause stays in the chain |
| `ErrNoQuotaDomain` | unavailable | the lane has no quota domain |
| `ErrLeaseUnknown` | not found | `RepoOfLease` on an unknown lease |
| `ErrNoAllowedLanes` | policy denied | no entitled lane of the right kind (unchanged) |

`errors.Is` on these compares the kind only: test a sentinel by identity and
read the message, which names every removed lane and its type.

### Commit, Abort, Release, ExpireStale

`Commit` confirms a spawn (held to committed). `Abort` is for a failed spawn:
it rolls a held reservation back and returns nil on a second call. The Reserver
has no rollback entry point, so `Abort` fails a `Bind` on the row (its own
rollback path); a parked or committed reservation is retired through `Release`.
`Commit`, `Abort` and `Release` refuse a lease whose reservation id does not
match its execution; `Abort` and `Release` also ignore the caller's
cancellation. `Release` runs the terminal teardown (worktree, then leases) and
returns nil on a second call. `ExpireStale` calls `Reserver.ExpireStale` with
`FleetDeps.TTL` as the liveness interval, so there is one expiry rule, then
drops the lease-index entry of every lease whose reservation is gone, replaced
or terminal (a parked lease stays indexed). `ReservationByExecution` reads an
execution's reservation through `FleetDeps.Reservations`.

### Selector seam

`Selector.Select(ctx, SelectInput)` chooses the tier and the lane from
`SelectInput.Eligible` (sorted, filtered, never nil). The interim
`NewTierPolicySelector` keeps the eligible lanes schedulable now on their own
observation (a fresh available state or a constrained one, profile outside
its backoff), projects them into tier slots (`ProjectTiers`), asks
`TierPolicy.SelectTier` (the one tier walk, jump rule and reserve flag) for the
tier and takes the first kept lane of it in lane-id order. A lane with unknown
or stale-available state is never leased and never probed: a probe stays in
flight until a dispatcher classifies it. A call reads its clock, scores and backoff
answers once. A later `economics.Rank` selector replaces it and owns probes.

### RepoOfLease

`RepoOfLease(leaseID)` answers which repository a live lease was issued for.
The repo is only what the caller put on `DispatchRef.RepoID`; it is never
derived from a path, a name, the job or the globs. It answers
`(repo, true, nil)` for a live lease with a repo and `("", false, nil)` for a
live lease issued with none. An unknown, released, aborted or pre-restart
lease, or one whose reservation is no longer held or committed, is
`ErrLeaseUnknown`; so is one whose reservation row names another repo than the
lease. An empty id is `KindInvalidInput`; a reservation read error is returned
as it is. A parked lease answers `ErrLeaseUnknown` while it is parked but
stays in the index (`ExpireStale` forgets only retired leases), so it answers
again after `Unpark`. The lease index is in memory and not persisted,
so after a restart every earlier lease id is unknown (fail closed).
