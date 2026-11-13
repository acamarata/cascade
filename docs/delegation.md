# Delegation: agent processes and the daemon socket

This page covers the daemon's delegation verbs, the order a delegation runs in,
where every value it trusts comes from, what happens across a restart, and how
the daemon treats the processes that agent spawns start (ADR-0006 section 3).

## Verbs

Both verbs are served on the daemon socket by the `delegate` daemon wiring
(conductor phase, order 50). Every refusal is a typed error `{kind, message}`,
never a raw Go error string. Params are strict JSON: unknown fields and
trailing data are refused as `invalid-input`.

`conductor.delegate`

| Param | Type | Notes |
| --- | --- | --- |
| `intent` | string | Required, at most 64 KiB. |
| `task_class` | string | Optional, one of the conductor's closed set. Default `code` for a mutation, `reason` otherwise. |
| `min_quality` | string | Optional. Default `standard`. |
| `deadline` | string | Optional. Default `interactive`. |
| `mutation` | bool | A mutation gets the full tool policy; everything else is read-only. |
| `job_id` | string | Required when `mutation` is true. |
| `wait_timeout_ms` | int | Optional, not negative. |

Result: `{job_id, status: "dispatched"}`. `job_id` is the delegation record ID.
A mutation delegation is refused `unavailable` for now: the daemon does not yet
resolve the job's worktree binding.

`conductor.delegate_wait`

Params `{job_id, timeout_ms}`; result
`{job_id, state, output?, artifacts?, data_class}`. The result is the addressed
notification correlated to `job_id`. At the timeout (capped at 10 minutes) the
stored record state comes back instead. Only the delegation's origin may wait:
the principal that delegated, or the owner CLI for an owner delegation. Anyone
else is refused `permission-denied`, and a refused wait neither reads nor
consumes the result.

## Access class

Both verbs are declared `AccessOwnerView` with `rpc.DeclareAccess` in the verb's
own file. The owner CLI may call them. A principal caller may call them only
with its own token, and it delegates from its own stored delegation record: a
principal with no record is refused `permission-denied`. The owner-CLI marker
is judged by the socket peer's process (next sections), so an agent cannot
reach the owner view by dropping its token.

## Order of one delegation

1. Caller from the request context (principal or owner CLI).
2. `ClassifyDelegation` applies the defaults above and picks the tool policy.
3. Job scope check: a principal's job must belong to its scope project, else
   `permission-denied` before any lease. The owner CLI may name any job.
4. Parent: the caller's own record, or for the owner CLI a fresh owner root
   and first hop signed with the installation key over an empty 0700 directory.
5. `VerifyChain(parent)` against the pinned installation public key, then
   `MintChild` narrows the envelope, and the child record is inserted live.
6. Repository id (mutations only) through the daemon's repository identity
   binder. An unregistered repository leases with an empty repo id; any other
   resolver error refuses the delegation before any lease.
7. `Fleet.Lease` of an `agent:<driver>` lane. A lane whose posture has no
   programmatic entitlement is aborted and refused.
8. Worktree: created and minimized to the in-scope paths, or an empty 0700
   directory for a read-only delegation.
9. A child session principal is minted; its session id is the driver's session
   id for the job.
10. A lease marker is created and bound to the record before the spawn, and the
    spawn is registered in the daemon's live-spawn registry.
11. `PreSpawnGates`, then the shim spawns with the projected envelope, the
    lane's model and effort, the principal token, the lease marker, the lease
    id and the child principal ID. Either id empty: abort, refuse, revoke.
12. `BindDelegationSpawn`, then `Fleet.Commit`.
13. The supervised collector waits for a terminal state or the deadline (the
    earliest of the envelope expiry, the budget wall clock and
    `[agents.delegation] max_wall_clock`), collects, delivers the addressed
    result, finishes the record, then releases in order: spawn handle,
    worktree, lease, principal.

Any failure after the lease and before the commit aborts the lease; after the
commit it releases it. The record always reaches a terminal state.

## Named sources

Nothing below is ever taken from RPC params.

| Value | Source |
| --- | --- |
| Envelope and root | Signed with the installation key; verified against the pinned installation public key. |
| Parent record | The caller's stored record, or the daemon's owner root for the owner CLI. |
| Tool policy | `ClassifyDelegation`: read-only unless `mutation`. |
| Sensitivity | The parent envelope, narrowed by `MintChild`; owner roots are `internal`. |
| Egress | The daemon's egress firewall. |
| Lane | The Fleet lease over the lanes registered from `[agents.lanes.<driver>]`. |
| Model and effort | `[agents.lanes.<driver>] model` and `effort`; empty is the driver default. |
| Wall clock | `[agents.delegation] wall_clock` (default 30m), capped by `max_wall_clock` (at most 24h). |
| Repository id | The daemon's repository identity binder, never a path, name or job id. |

## Recovery

At start, before either verb serves, the wiring runs `RecoverPendingSpawns`:
every pending spawn effect without a terminal outcome is marked
unknown-outcome (it is never replayed), and every live delegation record has
its lease marker swept, its worktree released, its lease released (aborted
when it was never committed), and is finished `failed` with reason
"daemon restart". A cleanup that fails leaves that record live for the next
start; other records are still processed.

## Limits

- The result inbox is in memory. A result that was delivered but not yet read
  is lost when the daemon stops.
- A restart fails every live delegation ("daemon restart"); nothing resumes.
- Mutation delegations are refused until the job worktree binding is wired.
- `delegate_wait` waits at most 10 minutes per call.

## Agent processes may not act as the owner

ADR-0006 section 3 says a request that claims the owner CLI is only as
trustworthy as the process behind the socket. An agent that drops its own
principal token (`env -u CASCADE_PRINCIPAL_TOKEN cascade ...`) sends the same
owner-CLI marker the owner's terminal sends. The daemon therefore judges every
owner-CLI request without a token by the process that opened the connection:

- A process that carries a live spawn's lease marker (`CASCADE_LEASE_ID`), sits
  in a live spawn's process group or session, or has a live spawn's pid in its
  ancestor chain, is refused with HTTP 403 and a `permission-denied` error,
  "agent processes may not act as the owner".
- A process the daemon cannot inspect, or whose pid it cannot resolve, is
  refused too.
- A request that carries a principal token is never judged by pid. An agent
  works through its own token, with the scope it was minted with.

Every agent spawn the daemon starts must be registered in the daemon's
live-spawn registry before the child runs, and released only after its lease
sweep returned nil. A spawn that is not registered is not recognised.

`TestDelegatedChildCannotActAsOwner` (`internal/delegation`, integration tag)
proves it end to end: a child the delegation service spawned, with its lease
marker set and `CASCADE_PRINCIPAL_TOKEN` dropped, calls `conductor.delegate`
over the daemon socket as the owner CLI and is refused before the verb runs.

## What this does not stop

The check narrows an agent's reach. It is not isolation. A descendant that left
the spawn's tree (double fork, environment cleared, `setsid`, reparented to
init or launchd) is not recognised. On macOS same-uid code can read the
daemon's environment through `KERN_PROCARGS2`. Both are tracked as
DEF-P2-agent-owner-reach, with OS isolation planned for P2.

Details, residuals and the tests that prove each claim:
[agent owner reach](security-posture/agent-owner-reach.md).
