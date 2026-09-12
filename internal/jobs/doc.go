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
// # DAG planner and risk classifier (AC/S-59.T4)
//
// Planner.Plan(ctx, PlanInput, SessionScope) (planner.go) turns a
// ticket-or-intent input into a single-node ExecutionDag (dag.go):
// DagNode carries EXACTLY {Capabilities, Deps, MutableScope, RiskClass,
// MinTaskClass, NodeRequirements, Timeout, CostCeiling, Priority} plus
// its id. PlanInput (planinput.go) is the ticket|intent union;
// TicketInput/IntentInput carry five pass-through fields the planner
// never synthesizes. A dependency cycle, an unknown model_class, and an
// empty input are typed KindInvalidInput errors.
//
// risk.go implements the R-21.182 footprint union (pre-image, post-image
// and an optional injected ReachabilityFn -- nil in W6) with its
// unlowerable Critical floor, then the R-16.37 Critical->High->Low->else
// Normal rules. riskgates.go maps RiskClass to the DECIDED GateSet.
// risk_reclassify.go's Reclassify re-runs the classifier over the
// ACTUAL changed-path set at every lease checkpoint (R-21.146):
// monotonic (never a downgrade), returning a typed Escalation on
// increase and the lease's out-of-scope changed paths independently.
//
// Consumers: T5's scheduler ("advance(dag, event)") consumes
// ExecutionDag; S-60.T2's job templates, S-60.T3's completion gates and
// AH/S-69.T1's policy-table-as-data form consume RiskClass + GateSet;
// AH/S-69.T3 compiles PEWS 17-field tickets into PlanInput (this
// package never imports plugins/pbd). AP/S-82.T1 (W9, R-21.39) aliases
// the conductor.plan RPC name over Planner.Plan; the planner itself
// gains no RPC surface of its own.
//
// # Job templates (AC/S-60.T2)
//
// template.go/template_kinds.go's six typed templates (implement,
// review, adversarial, qa, ci, integrate) resolve a TemplateContext
// (carried on ctx, since JobTemplate.Resolve takes no second parameter)
// into a DagNode. TemplateRegistry.By resolves a kind name;
// TemplateRegistry.Register is exported and additive, so AH/S-69.T2
// extends the same registry rather than building a parallel one.
//
// # Lifecycle stages and the risk-gate overlay (AH/S-69.T1)
//
// ONE risk model, per R-16.70(b): this package's own risk.go
// (classifier) and riskgates.go (the RiskClass -> GateSet DATA table,
// GateItem enum) are the ONLY representation of the risk-class gate
// mapping. AH/S-69.T1 owns exactly two things layered on top, neither a
// second table: lifecycle_stage.go's LifecycleStage (the 13 R-16.13
// dev-shop stages, DATA, plus ReclassificationStages -- the three
// R-21.146 mandatory reclassification points, {StagePlan, StageImplement,
// StageAccept}); and riskgates_overlay.go's RiskGateOverlay, a
// TIGHTENING-ONLY `[policy.risk_gates]` config overlay (additions only --
// the type cannot express a removal) whose EffectiveGateSet unions
// riskgates.go's own GateSetForRiskClass result with the overlay's
// per-class additions, table order then overlay order, de-duplicated.
// risk_reclass.go's RaiseRiskClass is the R-21.146 monotonic guard
// (prior/observed, not planned/actual -- risk_reclassify.go's own
// Reclassify is the different, pre-existing checkpoint entry point;
// RaiseRiskClass's result type is RiskEscalation, distinct from
// Reclassify's Escalation, because the two functions serve different
// call sites with different field needs -- see the S-69.T1 journal for
// why they could not share one type).
//
// internal/policy cannot import this package (internal/jobs already
// imports internal/conductor -> internal/hooks/egress ->
// internal/secrets -> internal/policy, so the reverse edge would cycle):
// internal/policy/risk_gates_config.go parses [policy.risk_gates] into a
// RAW map[string][]string, and BuildRiskGateOverlay (riskgates_overlay.go)
// is where the typed, gate-step-name-validated RiskGateOverlay is built
// -- called from cmd/cascade, the first layer that can import both
// packages, before a config reload swap (validate-before-write).
//
// `cascade policy risk explain <path...>` (cmd/cascade/policy_risk.go)
// classifies the R-21.182 union footprint through ClassifyFootprint (an
// exported wrapper over risk.go's own classifyFootprint, never a
// re-derivation) and reports EffectiveGateSet's result with each gate's
// provenance (table default vs overlay). It ships CLI-only: the ticket
// contract's JSON-RPC mirror could not be added to internal/policy/rpc.go
// without either breaking that package's own handler/verb-registry
// invariant test or skipping the R-21.207 Authorize middleware on a new
// daemon-exposed method, so it was left unshipped rather than shipped
// unauthenticated (see the S-69.T1 journal).
//
// # Scheduler (AC/S-59.T5)
//
// scheduler_types.go/scheduler.go/scheduler_admit.go/scheduler_resume.go
// implement Scheduler.Advance(ctx, dag, event, jobStates, activeLeases,
// governorFn) ScheduleDelta: a PURE function -- no side effects, no DB
// calls, no lease acquisition -- that admits non-contending DagNodes in
// parallel under the injected GovernorFn (the ONE admission seam,
// func(context.Context, governor.AdmissionRequest) (governor.Permit,
// error); this package imports only those two types from
// internal/fleet/governor, never the concrete AdmissionController),
// respects DagNode.Priority (descending, then node id ascending as the
// deterministic tiebreak), and idempotently cancels a job (CancelRequested
// on any non-terminal state emits exactly one ->cancelling transition
// plus an outbox intent; on a terminal state or cancelling itself it is a
// no-op; the terminal ->cancelled transition is emitted only on
// TerminationConfirmed). LeaseExpired never mutates state -- it appends
// an AttentionRaised Event to EventsToEmit -- and an expired_unconfirmed
// lease still counts as active for admissibility until a LeaseReclaimed
// event frees its scope (R-21.139/R-21.177). An unknown Event type is a
// typed KindInvalidInput error in ScheduleDelta.Errors, never a panic.
//
// Scheduler.Resume(ctx, store, journal, heartbeatInterval, probes,
// compensate) reconciles the R-21.148 transactional outbox (outbox.go,
// migration_outbox.go) by idempotency key, reaps executions whose
// heartbeat_at exceeds three heartbeatInterval periods into `abandoned`,
// then re-enters `running` jobs whose lease has passed ttl+expiry_grace
// (2h10m, R-16.37) at `leased` -- but ONLY when the Journal Reader seam
// (no direct import of internal/fleet/journal from this package; the
// caller wires it to the real M/S-27.T1 store) shows a real replayed
// trail for that job, and never for a lease still expired_unconfirmed.
// Advance and Resume both run only on the controller
// (scheduler_controller.go's Guard, wired onto internal/nodes'
// R-21.169 Role/RequireController primitive -- see the ticket journal
// for why this ticket reuses that existing guard rather than a second
// advisory-lock mechanism).
//
// # PEWS compiler (AH/S-69.T3)
//
// CompileTicket(PEWSContract, RiskGateOverlay) (PlanInput, GateSet, error)
// is the pure PEWS 17-field-to-PlanInput compiler AC/S-59.T4 named this
// package as the boundary consumer for. PEWSContract mirrors the
// N/S-28.T1 schema's 17 fields field-for-field without importing
// plugins/pbd/internal/pews -- 02-TARGET-STRUCTURE's v1.1 import-boundary
// rule runs plugins/providers -> pkg only, and Go's own internal/
// visibility rule additionally makes that package unreachable from here
// regardless. The party holding a decoded pews.Ticket builds a
// PEWSContract from it field-for-field; that caller is a downstream
// integration point (AH/S-70.T1), not this package.
//
// The R-21.192 field map is NORMATIVE: id, depends_on and the
// files_scope ADD+CHANGE+DELETE union map onto TicketInput{ID,
// DependsOn, Footprint} verbatim -- AC/S-59.T4's own shape, no new
// PlanInput variant. Every other row (title, short_desc/full_desc,
// branch, weight, tasks, checks, acceptance_criteria, spec_refs,
// sport_updates, docs_updates) has its own named mapping function in
// pews_compiler_fieldmap.go returning the value in its documented
// job/DAG target shape, even where no concrete Go type for that target
// exists yet in this tree (job metadata, verification jobs, an
// integrate job) -- those rows exist for a future job-metadata consumer
// and are exercised by test, per this ticket's own explicit descope of
// scheduling/leases/worktrees/CLI/RPC/YAML decoding.
//
// The CRLevel/QALevel -> RiskClass derivation covers the complete
// R-16.42 canonical form set: CR-B or CR-A+CR-B, and QA-A or QA-B, are
// Normal; CR-B+CR-C or CR-A+CR-B+CR-C, and QA-C, are High. The resolved
// class is the higher of the two (risk.go's own severity ranking);
// RiskClassCritical is never derivable from levels -- Critical is a
// footprint/domain classification (AC/S-59.T4 rule 1), never a
// cr_level/qa_level combination. DeclaredGateSet resolves that class
// through the ONE risk-gate table (GateSetForRiskClass), tightened by
// an AH/S-69.T1 RiskGateOverlay -- no second gate-set table exists here
// (R-16.70(b)).
//
// EffectiveTicketGateSet(declared, classifierDerived GateSet) is the
// R-21.192 union responsibility this compiler does NOT resolve itself:
// the classifier-derived set needs Planner.Plan's result, which needs a
// SessionScope no PEWSContract carries, so the caller runs Planner.Plan
// separately and unions its result with CompileTicket's declared set
// here. The classifier-derived set is an UNLOWERABLE FLOOR -- every one
// of its members survives into the result regardless of what the
// ticket declares -- and an empty classifierDerived refuses with
// ErrUnclassifiedFootprint rather than falling back to the declared set
// alone.
//
// Every one of the seventeen fields is required: ValidateContractFields
// fails closed on a missing (nil slice, or empty required string) or
// unparseable field with ErrMissingContractField or
// ErrUnparseableContractField naming the field, ahead of the more
// specific ErrEmptyTicketID / ErrUnknownModelClass / ErrUnknownReviewLevel
// sentinels for id, model_class, cr_level and qa_level. CompileTicket
// never returns a partial PlanInput alongside an error.
//
// # Scope boundaries
//
// This package ships the schema, records, state machine, store, DAG
// planner, risk classifier, job templates, lifecycle stages, the
// risk-gate overlay, the PEWS compiler, and (AC/S-59.T5) the
// scheduler/admission/resume/outbox. It does NOT implement: the lease
// model's fence check/reclaim (AC/S-59.T2), the worktree manager
// (AC/S-59.T3), the DAG-assembly CLI/RPC surface (AC/S-60.T1), the
// completion gate that acts on Reclassify's or RaiseRiskClass's result
// (AC/S-60.T3) -- verifying/reviewing/accepted transitions are that
// engine's alone; the scheduler never emits them -- or a live
// plugins/pbd caller for the PEWS compiler (AH/S-70.T1). No
// ci_attestation table exists here -- AF/S-65.T4 owns it.
package jobs
