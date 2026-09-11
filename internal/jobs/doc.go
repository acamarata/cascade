// Package jobs owns the `jobs` cascade.db domain (R-14.5, Epic AC):
// seven tables -- job, task_dependency, execution, execution_result,
// artifact, resource_lease, worktree -- created via the B/S-02.T3
// portable migration builder (see MigrationSet in migration.go) and
// registered as the domain's owner package in internal/storage/
// domains.go's AllDomains.
//
// # Records
//
// model.go declares the seven tables' Go shapes, plus the two closed
// vocabularies this ticket owns the decode of: ConsequenceClass
// (R-21.99: trivial/normal/consequential) and DataClass (R-21.94: the
// request floor, public/internal/confidential/secret). Job.RiskClass and
// Job.MinTaskClass stay plain strings -- their closed sets, if any, are
// owned by other tickets (AC/S-59.T4 and K/S-22.T4 respectively),
// matching pkg/provider.ModelRequest.TaskClass's own precedent for the
// same reason.
//
// # State machine
//
// state.go implements the R-16.37 JobState enum as amended by R-21.140:
//
//	pending -> leased -> running -> verifying -> reviewing ->
//	    accepted | rejected
//	any non-terminal -> cancelling -> cancelled
//	any non-terminal -> failed
//
// Unknown/unparseable state decodes to failed (no permissive zero
// value). Four transitions -- running->verifying, verifying->reviewing,
// reviewing->accepted, reviewing->rejected -- are POLICY-RESERVED: only
// internal/policy may invoke them (PolicyTransitionAllowed); the public
// store path (Store.PutTransition, gated by TransitionAllowed) refuses
// every one of them with a typed permission error.
//
// # Store
//
// Store (split across store_job.go, store_exec.go and store_lease.go
// under the 300-line cap) is the persisted CRUD surface. It
// store-enforces:
//   - an execution whose job_id does not exist: a typed orphan-execution
//     error (IntegrityCheck audits for orphans directly);
//   - a job/lease state write outside the legal transition table: a
//     typed unknown-transition error;
//   - an artifact update that would change its DataClass: refused (the
//     column is immutable per R-21.94);
//   - a resource_lease update that would lower its Epoch: refused (the
//     column is monotonic per R-21.139).
//
// # Scope boundaries
//
// This package ships the schema, records, state machine and store only.
// It does NOT implement: the lease model's fence check/reclaim
// (AC/S-59.T2), the worktree manager (AC/S-59.T3), the DAG planner
// (AC/S-59.T4), the scheduler/admission (AC/S-59.T5), or any CLI/RPC
// surface (AC/S-60.T1). No ci_attestation table exists here -- AF/S-65.T4
// owns it.
package jobs
