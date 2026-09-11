# Jobs, DAG, Leases

The `jobs` cascade.db domain (R-14.5, owned by Epic AC) hosts seven
tables, materialized by `internal/jobs` via the shared portable migration
builder.

## Schema

| Table | Purpose | Key fields |
|---|---|---|
| `jobs_job` | one row per DAG-node job | id, state, capabilities[], mutable_scope, risk_class, min_task_class, node_requirements, timeout_seconds, cost_ceiling, priority |
| `jobs_task_dependency` | deps[] edges | job_id, depends_on_job_id |
| `jobs_execution` | one row per attempt (R-16.33) | id, job_id, attempt, state, started_at, ended_at |
| `jobs_execution_result` | one outcome per execution | execution_id, output_summary, error_kind, error_message, artifact_refs[] |
| `jobs_artifact` | one produced file/blob | id, job_id, execution_id, kind, blob_key (BLAKE3), path |
| `jobs_resource_lease` | the one-mutable-writer lease record | repo_id + scope_glob (natural key), holder, issued_at, ttl_seconds, renew_count, journal_ref |
| `jobs_worktree` | one checked-out working tree | path (key), lease ref, repo, branch |

No `jobs_ci_attestation` table exists; AF/S-65.T4 owns it.

## Job state machine (R-16.37, amended by R-21.140)

```
pending -> leased -> running -> verifying -> reviewing -> accepted
                                                         -> rejected
any non-terminal -> cancelling -> cancelled
any non-terminal -> failed
```

Unknown or unparseable state decodes to `failed` — never a permissive
zero value. Four transitions are **policy-reserved** and refused on the
public store path with a typed error: `running->verifying`,
`verifying->reviewing`, `reviewing->accepted`, `reviewing->rejected`.
Only `internal/policy`, through `PolicyTransitionAllowed`, may invoke
them (S-60.T3 wires the real caller).

## W9 columns (inert here; semantics owned elsewhere)

- `jobs_job.consecutive_failed_attempts` (R-21.84) — increment/reset/wake
  debounce owned by AP/S-81.T3.
- `jobs_job.consequence_class`, `jobs_artifact.consequence_class`
  (R-21.99, closed set `trivial`/`normal`/`consequential`) — derivation
  from the AC/S-59.T4 risk class owned by AQ/S-83.T4.
- `jobs_job.data_class` (request floor) and `jobs_artifact.data_class`
  (IMMUTABLE) (R-21.94) — lattice join owned by AQ/S-83.T1-T4.

## W6 fencing + liveness columns (inert here; semantics owned elsewhere)

- `jobs_resource_lease.epoch` (R-21.139, monotonic per repo id + scope)
  and `.state` (closed set `held`/`renewing`/`expired_unconfirmed`/
  `expired_orphaned`/`released`) — the fence CHECK and reclaim are
  AC/S-59.T2's.
- `jobs_execution.pgid` and `.heartbeat_at` (R-21.140/R-21.172), plus the
  `abandoned` execution state — the liveness probe and quarantine sweep
  are AC/S-59.T3's; the heartbeat reaper is AC/S-59.T5's.
- `JobState`'s non-terminal `cancelling` — the sole gateway into
  `cancelled` — with the verified-termination gate on
  `cancelling->cancelled` owned by AC/S-59.T5 (driver side: AD/S-61.T1).

## Migration and owner registration

`internal/jobs.MigrationSet()` claims schema_version 5 in cascade.db's
single global version sequence (bootstrap=1, context/scope=2,
retrieval/lifecycle=3, providers/registry=4). `internal/storage/
domains.go`'s `AllDomains` entry for `DomainJobs` now names
`internal/jobs` as the owner package.

## Ownership boundaries

This domain ships the schema, records, state machine and typed store
only. It does not implement: the lease fence check/reclaim (AC/S-59.T2),
the worktree manager (AC/S-59.T3), the DAG planner (AC/S-59.T4), the
scheduler/admission (AC/S-59.T5), or any CLI/RPC surface (AC/S-60.T1).
