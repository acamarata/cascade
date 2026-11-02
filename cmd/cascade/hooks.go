//go:build !windows

// Purpose: compose the completion gate over the daemon's stores and event bus.
// Outputs: the completion-gate hook pack and fleet.sessions.completion_check.
// Constraints: acceptance uses the daemon dispatcher and real lease authority.
// SPORT: cmd/cascade/daemon (ADD, P1-E32-W6-S66-T1).
package main

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
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

// completionController resolves the on-disk role on every authorization.
func completionController(paths runtime.PathProvider) func() bool {
	return func() bool {
		role, err := nodes.ResolveRole(nodes.NewFileControllerBindingBackend(paths.DataDir()))
		return err == nil && role == nodes.RoleController
	}
}

// wireCompletionHookPack opens the jobs domain's own db handle (the same
// registerContextEngineHandlers/wireJobRPC "second sqlite connection to
// cascade.db" pattern buildRPCServer's own comments document, since
// rawDB is not threaded into this composition root), builds a real
// *jobs.CompletionPolicy, and registers both the completion-gate hook
// pack (descriptor side) and its live RPC handler (dispatch side).
func wireCompletionHookPack(ctx context.Context, registry *rpc.Registry, store provider.Store, clock runtime.Clock, bus *events.Bus, paths runtime.PathProvider, acceptance ...any) error {
	// Legacy startup-recovery callers do not mount a completion handler.
	if len(acceptance) == 0 {
		_, _, err := completionLedger(ctx, store, clock, paths, nil, nil)
		return err
	}
	if len(acceptance) != 4 {
		return cascade.New(cascade.KindInvalidInput, "completion gate: acceptance dependencies required")
	}
	verifier, _ := acceptance[0].(jobs.AcceptanceVerifier)
	checkpoints, _ := acceptance[1].(completionCheckpoints)
	leases, _ := acceptance[2].(*jobs.LeaseManager)
	isController, _ := acceptance[3].(func() bool)
	return registerCompletionGate(ctx, registry, store, clock, bus, paths, verifier, checkpoints, leases, isController)
}

// registerCompletionGate requires an exact-checkpoint source before mounting.
func registerCompletionGate(ctx context.Context, registry *rpc.Registry, store provider.Store, clock runtime.Clock, bus *events.Bus, paths runtime.PathProvider,
	verifier jobs.AcceptanceVerifier, checkpoints completionCheckpoints, leases *jobs.LeaseManager, isController func() bool) error {
	if checkpoints == nil {
		return cascade.New(cascade.KindInvalidInput, "completion gate: checkpoints required")
	}
	if v, ok := verifier.(*ciAcceptanceVerifier); ok && checkpoints != v.dispatcher {
		return cascade.New(cascade.KindInvalidInput, "completion gate: acceptance and checkpoints must use the same dispatcher")
	}
	ledger, jstore, err := completionLedger(ctx, store, clock, paths, leases, isController)
	if err != nil {
		return err
	}
	policy, err := jobs.NewCompletionPolicy(jobs.CompletionPolicyDeps{
		Store: jstore, Ledger: ledger, Bus: bus, Clock: clock, EngineID: completionGateEngineID,
		Acceptance: verifier, RequireAcceptance: true,
	})
	if err != nil {
		return err
	}
	gate := &completionPolicyGate{policy: policy, store: jstore, checkpoints: checkpoints}
	resolver := &completionJobResolver{store: jstore}
	if err := hookpacks.RegisterCompletionHookPack(hookpacks.DefaultRegistry, gate, resolver); err != nil {
		return err
	}
	return hookpacks.RegisterCompletionCheckHandler(registry, clock, bus, gate, resolver, 0)
}

// completionLedger opens the evidence store and binds producers to the lease authority.
func completionLedger(ctx context.Context, store provider.Store, clock runtime.Clock, paths runtime.PathProvider,
	leases *jobs.LeaseManager, isController func() bool) (*jobs.EvidenceLedger, *jobs.Store, error) {
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000&_txlock=immediate&_journal_mode=WAL&_synchronous=NORMAL")
	if err != nil {
		return nil, nil, cascade.Wrapf(cascade.KindUnavailable, err, "completion gate: open %s", dbPath)
	}
	db.SetMaxOpenConns(1)
	if err := jobs.ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		return nil, nil, err
	}
	if err := jobs.ApplyEvidenceSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		return nil, nil, err
	}
	// db is intentionally not closed here: it lives for the daemon
	// process's lifetime, the same disclosed gap wireJobRPC's own call
	// site (daemon_unix_run_fleetjobs.go) already carries.

	jstore := jobs.NewStore(db)
	authz := jobs.NewProducerAuthz(jstore, isController, leases)
	// The daemon binds the process egress firewall before its audit log opens
	// (openDaemonAuditLog); the effect log is then the verified view of that
	// same shared log.
	if _, err = openDaemonAuditLog(store, clock, nil, paths); err != nil { // result unused: openEffectLog reads the memoized log
		return nil, nil, err
	}
	effects, err := openEffectLog(store, clock, nil, paths)
	if err != nil {
		return nil, nil, err
	}
	ledger, err := jobs.NewEvidenceLedger(jstore, clock, effects, authz)
	if err != nil {
		return nil, nil, err
	}
	// Settle the intents a crash left open before the gate serves anything.
	if err := recoverEvidenceEffects(ctx, effects, ledger); err != nil {
		kind, ok := cascade.KindOf(err)
		if !ok {
			kind = cascade.KindUnavailable
		}
		return nil, nil, cascade.Wrap(kind, err, "completion gate: start-up recovery of open evidence audit intents failed")
	}
	return ledger, jstore, nil
}

// recoverEvidenceEffects settles every evidence intent a previous run left
// open (a crash between the audit intent and the row's confirmation): the
// ledger probes each row and confirms or fails the intent. Intents of other
// effect sites are left for their own recovery.
func recoverEvidenceEffects(ctx context.Context, effects *audit.EffectLog, ledger *jobs.EvidenceLedger) error {
	pending, err := effects.PendingEffects(ctx)
	if err != nil {
		return err
	}
	return ledger.RecoverEffects(ctx, pending)
}

// completionPolicyGate adapts *jobs.CompletionPolicy to
// hookpacks.PolicyGate. It is the concrete type completion_gate.go's own
// doc comment says never lives in that file.
type completionPolicyGate struct {
	policy      *jobs.CompletionPolicy
	store       *jobs.Store
	checkpoints completionCheckpoints
}

// completionCheckpoints supplies the dispatcher's current immutable binding.
type completionCheckpoints interface {
	CurrentCheckpoint(context.Context, string) (ci.CandidateSnapshot, bool, error)
}

// nextCompletionTarget selects the next reserved edge for the job's state.
func nextCompletionTarget(state jobs.JobState) jobs.JobState {
	if state == jobs.JobStateReviewing {
		return jobs.JobStateAccepted
	}
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
	req := jobs.TransitionRequest{
		Job:              &job,
		Target:           nextCompletionTarget(job.State),
		Caller:           jobs.PolicyEngineIdentity{EngineID: completionGateEngineID},
		PlannedRiskClass: risk,
	}
	if req.Target == jobs.JobStateAccepted {
		if job.RiskClass == "" {
			return false, "completion gate: job " + jobID + " has no risk class", nil
		}
		snap, found, checkpointErr := g.checkpoints.CurrentCheckpoint(ctx, jobID)
		if checkpointErr != nil || !found {
			return false, "completion gate: current checkpoint unavailable", nil
		}
		req.CheckpointID, req.TreeHash = snap.CheckpointID, snap.TreeHash
	}
	err = g.policy.Transition(ctx, req)
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
