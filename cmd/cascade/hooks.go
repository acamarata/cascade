//go:build !windows

// Purpose: the AF/S-66.T1 completion-gate composition root. It gives
//
//	internal/jobs.NewCompletionPolicy (P1-E29-W6-S60-T3, shipped fully
//	implemented and tested but with zero production callers -- see
//	testonly-allow.json and R-16.83) its first real caller: a
//	*jobs.CompletionPolicy built over this daemon's own jobs domain
//	tables, wired to the daemon's real event bus, and reached through the
//	real internal/fleet/hookpacks.PolicyGate seam.
//
// CONTRACT NOTE (files_scope, quoted in the journal, same shape as
// daemon_unix_jobs_rpc.go's own CONTRACT NOTE it cites): this file, and
// the one-line call this file's wireCompletionHookPack needs from
// buildRPCServer (daemon_unix_run.go), are not named in this ticket's
// files_scope. Per the AGENT-BRIEF's own instruction ("a registered RPC
// method must be MOUNTED at the composition root... never invent a new
// root"), this file exists because RegisterCompletionHookPack and
// RegisterCompletionCheckHandler would otherwise ship built, tested and
// unreachable from any real daemon -- exactly R-14.166/R-14.223's
// forbidden pattern, and the specific defect class R-16.83 named this
// ticket as the fix for. Named and exempted in .golangci.yml's
// cmd-rpc-server-boundary list and internal/client/boundary_test.go's
// cmdRPCBoundaryExempt map (edited together, per the AGENT-BRIEF): this
// file REGISTERS fleet.sessions.completion_check on the daemon's own
// registry and never dials the daemon, the identical daemon-SIDE
// reasoning daemon_unix_jobs_rpc.go already carries its own exemption
// under.
//
// Inputs: the real *rpc.Registry, provider.Store (for the real
//
//	audit.Writer -- the SAME store buildRPCServer's other call sites
//	share, never a second one), the process Clock, the daemon's own
//	*events.Bus, and the resolved PathProvider.
//
// Outputs: fleet.sessions.completion_check registered on registry, and
//
//	the "completion-gate" pack registered on hookpacks.DefaultRegistry
//	(P/S-34.T1's install wizard renders it from there, same as
//	"sessions").
//
// Constraints: Approvals and Attention are deliberately nil
//
//	(jobs.CompletionPolicyDeps' own doc comment: "acceptable for a
//	CompletionPolicy that only ever gates Normal/Low jobs, never for a
//	production composition that also handles Critical/High"). Wiring a
//	real policy.ApprovalQueue here would mean threading wirePolicy's
//	*policyWiring through this call site, which does not happen at HEAD
//	(buildRPCServer's own withPolicyHandlers option is the only consumer
//	today) -- recorded as a follow-up, not invented here.
//
// SPORT: cmd/cascade/daemon (ADD, P1-E32-W6-S66-T1).
package main

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// completionGateEngineID is the PolicyEngineIdentity every completion
// check this composition root evaluates is stamped with. It is compared
// against itself (jobs.CompletionPolicy.Transition's own caller-identity
// check, completion.go's header comment) -- this file constructs both
// ends, so the value only needs to be non-empty and stable.
const completionGateEngineID = "hookpacks.completion-gate"

// wireCompletionHookPack opens the jobs domain's own db handle (the same
// registerContextEngineHandlers/wireJobRPC "second sqlite connection to
// cascade.db" pattern buildRPCServer's own comments document, since
// rawDB is not threaded into this composition root), builds a real
// *jobs.CompletionPolicy, and registers both the completion-gate hook
// pack (descriptor side) and its live RPC handler (dispatch side).
func wireCompletionHookPack(ctx context.Context, registry *rpc.Registry, store provider.Store, clock runtime.Clock, bus *events.Bus, paths runtime.PathProvider) error {
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "completion gate: open %s", dbPath)
	}
	db.SetMaxOpenConns(1)
	if err := jobs.ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		return err
	}
	if err := jobs.ApplyEvidenceSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		return err
	}
	// db is intentionally not closed here: it lives for the daemon
	// process's lifetime, the same disclosed gap wireJobRPC's own call
	// site (daemon_unix_run_fleetjobs.go) already carries.

	jstore := jobs.NewStore(db)
	authz := jobs.NewProducerAuthz(jstore, func() bool { return true }, nil)
	ledger, err := jobs.NewEvidenceLedger(jstore, clock, audit.New(store, clock, nil), authz)
	if err != nil {
		return err
	}
	policy, err := jobs.NewCompletionPolicy(jobs.CompletionPolicyDeps{
		Store: jstore, Ledger: ledger, Bus: bus, Clock: clock, EngineID: completionGateEngineID,
	})
	if err != nil {
		return err
	}

	gate := &completionPolicyGate{policy: policy, store: jstore}
	resolver := &completionJobResolver{store: jstore}
	if err := hookpacks.RegisterCompletionHookPack(hookpacks.DefaultRegistry, gate, resolver); err != nil {
		return err
	}
	return hookpacks.RegisterCompletionCheckHandler(registry, clock, bus, gate, resolver, 0)
}

// completionPolicyGate adapts *jobs.CompletionPolicy to
// hookpacks.PolicyGate. It is the concrete type completion_gate.go's own
// doc comment says never lives in that file.
type completionPolicyGate struct {
	policy *jobs.CompletionPolicy
	store  *jobs.Store
}

// nextCompletionTarget names the policy-reserved edge a completion check
// evaluates from job's current state: running jobs are checked against
// verifying, and jobs already at verifying (a second completion claim,
// e.g. after a prior gate denial was fixed) are checked against
// reviewing. Any other current state is passed through unchanged so
// Transition's own PolicyTransitionAllowed produces the correct
// WrongSuccessor denial rather than this adapter guessing a nonsensical
// target.
func nextCompletionTarget(state jobs.JobState) jobs.JobState {
	if state == jobs.JobStateVerifying {
		return jobs.JobStateReviewing
	}
	return jobs.JobStateVerifying
}

// CompletionCheck implements hookpacks.PolicyGate over the real
// completion-gate engine.
func (g *completionPolicyGate) CompletionCheck(ctx context.Context, jobID string) (bool, string, error) {
	job, found, err := g.store.GetJob(ctx, jobID)
	if err != nil {
		return false, "", err
	}
	if !found {
		return false, "", cascade.Newf(cascade.KindNotFound, "completion gate: unknown job %q", jobID)
	}
	risk := jobs.RiskClass(job.RiskClass)
	if job.RiskClass == "" {
		risk = jobs.RiskClassNormal
	}
	err = g.policy.Transition(ctx, jobs.TransitionRequest{
		Job:              &job,
		Target:           nextCompletionTarget(job.State),
		Caller:           jobs.PolicyEngineIdentity{EngineID: completionGateEngineID},
		PlannedRiskClass: risk,
	})
	if err == nil {
		return true, "", nil
	}
	var denied *jobs.ErrorCompletionDenied
	if errors.As(err, &denied) {
		return false, denied.Error(), nil
	}
	return false, "", err
}

// completionJobResolver adapts *jobs.Store to hookpacks.JobResolver: it
// validates a caller-presented job id against the real jobs domain
// table. An empty payload.JobID resolves to ("", nil) -- the ordinary
// human-session case (R-21.176) -- without ever touching the store.
type completionJobResolver struct {
	store *jobs.Store
}

// ResolveJob implements hookpacks.JobResolver.
func (r *completionJobResolver) ResolveJob(ctx context.Context, payload hookpacks.CompletionHookPayload) (string, error) {
	if payload.JobID == "" {
		return "", nil
	}
	_, found, err := r.store.GetJob(ctx, payload.JobID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", cascade.Newf(cascade.KindNotFound, "completion gate: unknown job %q", payload.JobID)
	}
	return payload.JobID, nil
}
