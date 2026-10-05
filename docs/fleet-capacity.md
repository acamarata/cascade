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
only. `released` and `rolled_back` are terminal. Every state change goes through the store's transition, which is
compare-and-set on the stored state. A pure state move (park, unpark,
commit) writes the state column and nothing else. A progress write (step
ledger, handles, accounting, and the teardown that ends in a terminal
state) never writes the placement or the liveness columns. So a `Bind`
or a claim that lands after a writer read the row is never erased by that
writer.

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
