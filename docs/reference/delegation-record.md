# Delegation record

The stored, signed record of one agent delegation (contract:delegation-record).
The table lives in the one jobs database: package
[`internal/jobs`](../../internal/jobs), file `delegation.go`, schema in
`delegation_schema.go`. The minting, chain verification and pre-spawn gate
registry live in [`internal/delegation`](../../internal/delegation). The
envelope itself is the one [scope envelope](scope-envelope.md) (ADR-0007).
This package defines no envelope or budget type of its own.

## Record fields

| Field | Source |
|---|---|
| `ID` | The child envelope's `ID`. Must equal it on insert. |
| `EnvelopeDigest` | `envelope.Digest` of the decoded `Signed`. Derived. |
| `Signed` | The `envelope.Signed` JSON. Unique by digest, so a replay under a new ID is refused. |
| `ParentDigest` | The envelope's `ParentDigest`. Derived. Empty for a root record. |
| `ParentSigned` | The stored parent row's `Signed`. For a first hop, the root envelope the caller supplies, which must hash to `ParentDigest`. |
| `OriginPrincipal` | Inherited from the parent row. For a first hop or a root record, the root envelope's `Principal` (a principal ID or `owner-cli`). A caller value is ignored. |
| `OriginSession` | The daemon path's session. |
| `AgentPrincipal` | Must equal the envelope's `Principal`. Unique across the table. |
| `Depth` | The stored parent's depth plus one. A first hop under an unstored root is 1. A root record is 0. A caller value is ignored. |
| `JobID`, `Mutation`, `Lane` | The daemon path's job row. |
| `LeaseMarker` | Set once by `BindDelegationMarker`, before `Shim.Spawn` (M-4). |
| `LeaseID`, `AgentJobID` | Set once, together, by `BindDelegationSpawn`, after the marker. |
| `State`, `Reason` | `live` on insert; `FinishDelegation` moves it to one terminal state once. |
| `CreatedAt`, `UpdatedAt` | `CreatedAt` is the caller's injected-clock time and is required. `UpdatedAt` is set to it on insert. |

`UpdatedAt` residual: the contracted `BindDelegationMarker`, `BindDelegationSpawn`
and `FinishDelegation` signatures carry no time, and `Store` has no clock, so
they leave `UpdatedAt` unchanged. See the ticket's ruling draft.

## States

`live`, then exactly one of `succeeded`, `failed`, `cancelled`, `refused`.
A terminal row never changes again.

## Insert-only enforcement

SQLite triggers, added by `ApplyDelegationSchema`, refuse at the database
whatever path issues the statement:

| Trigger | Refuses |
|---|---|
| `jobs_delegation_immutable` | Any change to a record field (everything but the bindings, state, reason and `updated_at`). |
| `jobs_delegation_marker_once` | A second marker. |
| `jobs_delegation_spawn_once` | A second spawn binding. |
| `jobs_delegation_marker_first` | A spawn binding before the marker. |
| `jobs_delegation_terminal` | Any change to a terminal row. |
| `jobs_delegation_state_update` | An unknown state, a reason change while live, or a lease ID without an agent job ID. |
| `jobs_delegation_state_insert` | An insert that is not a fresh live row. |
| `jobs_delegation_no_delete` | Every DELETE. |

Another SQL dialect is refused (`KindUnsupported`) rather than left without
the triggers.

## Caps

`delegation.MaxDepth = 3` and `delegation.MaxLiveChildren = 8`. They alias
`jobs.DelegationMaxDepth` and `jobs.DelegationMaxLiveChildren`, because
`internal/delegation` imports `internal/jobs`. `InsertDelegation` reads the
parent and checks depth in one transaction. The live-children count is the
INSERT's own `WHERE` condition, so the check and the write are one statement
under SQLite's single writer.

## Named sources

`MintRoot` and `MintChild` build every field from `Sources`, which the daemon
path fills from the job row and config, never from RPC params. No parameter
takes a caller root, tool, egress or grant.

| Envelope field | Source |
|---|---|
| `ID` | A fresh `dlg_` + 32 hex from `crypto/rand`. |
| `Principal` | The owner's ID (or `owner-cli`) for a root; the child principal ID for a child. |
| `Roots` | `RepoRoot` joined with each `InScope` prefix (mutation), or `ReadOnlyDir` alone (read-only). An absolute, escaping, glob or backslash `InScope` entry is refused. |
| `Tools` | Exactly `["agent.tools.<ToolPolicy>"]`; `ToolPolicy` is `none`, `readonly` or `all` (m-10). |
| `Commands` | None. |
| `Egress` | `["agent-driver"]`. |
| `Grants` | None in P1. |
| `Budget` | `Sources.Budget` (from `[agents.delegation]` defaults). |
| `Sensitivity` | Root: `Sources.Sensitivity`. Child: `JoinSensitivity(Sources.Sensitivity, parent)`. |
| `IssuedAt`, `ExpiresAt` | `Now` and `Now + TTL`. A root is capped at the owner principal's expiry. |
| `Fence`, `Issuer`, `Version`, `Ext` | Child only: copied from the parent. |
| `ParentDigest` | Child only: `Digest(parent)`. |

`MintChild` verifies the parent under the pinned key at `Sources.Now`, then
signs only when `envelope.Narrow(parent, child)` passes. A wider child returns
the `*envelope.NarrowError` naming the field.

## Verification order

`VerifyChain(ctx, st, rec, pinned, now)`:

1. Re-read `rec` from the store. The caller's copy must equal the stored row.
2. The record must be `live`, with fewer than `MaxLiveChildren` live children.
3. For the record and then each ancestor, up to `MaxDepth` hops:
   1. depth at most `MaxDepth`;
   2. its ID and digest not seen lower in the chain (cycle);
   3. `envelope.Verify` of `Signed` under the pinned key at `now`;
   4. `Digest(env) == EnvelopeDigest`, and the envelope's ID and `ParentDigest` match the row;
   5. a root record stops here with depth 0 and no `ParentSigned`;
   6. `envelope.Verify` of `ParentSigned`, `Digest(parent) == ParentDigest`, the parent's ID and digest not seen (cycle), and `envelope.Narrow(parent, child)`;
   7. the parent's stored row must equal `ParentSigned` and sit one level up. An unstored parent is accepted only as a root envelope above a depth-1 record.

Tampering refuses with `KindIntegrity`. Depth, fan-out, narrowing, expiry and
state refuse with `KindPermissionDenied`. Each message names its rule.

## The fence rule

`Narrow` requires the child's `Fence` to equal the parent's, so a delegated
child keeps its parent's fencing token. The child's own Fleet lease is bound
on the record (`LeaseID`), not in the envelope.

## Pre-spawn gates

`RegisterPreSpawnGate(name, gate)` registers a `PreSpawnGate` at init time and
returns `struct{}`, so a package can write `var _ = RegisterPreSpawnGate(...)`.
An empty name, a nil gate or a duplicate name panics. `PreSpawnGates()`
returns the gates sorted by name. The delegate path runs them after the
marker is bound and before `Shim.Spawn`. P1-EXE-04 registers its budget debit
here.

## Wiring

`ApplyDelegationSchema` is not yet called by the daemon's jobs-store
start-up. The first consumer that stores a record wires it into that
start-up, next to the other jobs sets.
