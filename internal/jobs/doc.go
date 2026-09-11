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
// # Scope boundaries
//
// This package ships the schema, records, state machine, store, DAG
// planner, risk classifier, job templates, lifecycle stages and the
// risk-gate overlay. It does NOT implement: the lease model's fence
// check/reclaim (AC/S-59.T2), the worktree manager (AC/S-59.T3), the
// scheduler/admission (AC/S-59.T5), the DAG-assembly CLI/RPC surface
// (AC/S-60.T1), or the completion gate that acts on Reclassify's or
// RaiseRiskClass's result (AC/S-60.T3). No ci_attestation table exists
// here -- AF/S-65.T4 owns it.
package jobs
