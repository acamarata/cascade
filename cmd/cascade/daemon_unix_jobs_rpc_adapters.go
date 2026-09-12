//go:build !windows

// Purpose: the four adapter types daemon_unix_jobs_rpc.go's wireJobRPC
//
//	builds — jobStoreAdapter/leaseStoreAdapter/controllerGuardAdapter/
//	outboxAdapter — translating internal/rpc's duck-typed JobStore/
//	LeaseStore/ControllerGuard/OutboxRecorder seams onto the real
//	*jobs.Store/*jobs.LeaseManager/nodes.Role/jobs.RecordIntent+
//	ConfirmEffect. Split from daemon_unix_jobs_rpc.go purely to keep
//	both files under the 300-line cap.
//
// SPORT: cmd/cascade/daemon (ADD, P1-E29-W6-S60-T1).
package main

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

// jobRecordFrom converts a real jobs.Job into rpc.JobRecord.
func jobRecordFrom(j jobs.Job) rpc.JobRecord {
	return rpc.JobRecord{
		ID: j.ID, State: string(j.State), CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt,
		Capabilities: j.Capabilities, MutableScope: j.MutableScope, RiskClass: j.RiskClass,
		MinTaskClass: j.MinTaskClass, NodeRequirements: j.NodeRequirements,
		TimeoutSeconds: j.TimeoutSeconds, CostCeiling: j.CostCeiling, Priority: j.Priority,
		ConsecutiveFailedAttempts: j.ConsecutiveFailedAttempts,
		ConsequenceClass:          string(j.ConsequenceClass), DataClass: string(j.DataClass),
	}
}

// jobStoreAdapter implements rpc.JobStore against a real *jobs.Store.
type jobStoreAdapter struct{ store *jobs.Store }

func (a jobStoreAdapter) ListJobs(ctx context.Context, q rpc.JobListQuery) (rpc.JobPage, error) {
	rows, cursor, err := a.store.ListJobs(ctx, jobs.JobFilter{
		ScopeGlob: q.ScopeGlob, State: jobs.JobState(q.State), Limit: q.Limit, Cursor: q.Cursor,
	})
	if err != nil {
		return rpc.JobPage{}, err
	}
	out := make([]rpc.JobRecord, 0, len(rows))
	for _, j := range rows {
		out = append(out, jobRecordFrom(j))
	}
	return rpc.JobPage{Jobs: out, Cursor: cursor}, nil
}

func (a jobStoreAdapter) GetJob(ctx context.Context, id string) (rpc.JobRecord, bool, error) {
	j, ok, err := a.store.GetJob(ctx, id)
	if err != nil || !ok {
		return rpc.JobRecord{}, ok, err
	}
	return jobRecordFrom(j), true, nil
}

func (a jobStoreAdapter) CancelJob(ctx context.Context, id string, now int64) (rpc.JobRecord, error) {
	if err := a.store.PutTransition(ctx, id, jobs.JobStateCancelling, now); err != nil {
		return rpc.JobRecord{}, err
	}
	j, _, err := a.store.GetJob(ctx, id)
	return jobRecordFrom(j), err
}

func (a jobStoreAdapter) RetryJob(ctx context.Context, orig rpc.JobRecord, newID string, gen int64, now int64) (rpc.JobRecord, error) {
	newJob := jobs.Job{
		ID: newID, State: jobs.JobStatePending, CreatedAt: now, UpdatedAt: now,
		Capabilities: orig.Capabilities, MutableScope: orig.MutableScope, RiskClass: orig.RiskClass,
		MinTaskClass: orig.MinTaskClass, NodeRequirements: orig.NodeRequirements,
		TimeoutSeconds: orig.TimeoutSeconds, CostCeiling: orig.CostCeiling, Priority: orig.Priority,
		ConsecutiveFailedAttempts: gen,
		ConsequenceClass:          jobs.ConsequenceClass(orig.ConsequenceClass),
		DataClass:                 jobs.DataClass(orig.DataClass),
	}
	if err := a.store.PutJob(ctx, newJob); err != nil {
		return rpc.JobRecord{}, err
	}
	return jobRecordFrom(newJob), nil
}

// leaseStoreAdapter implements rpc.LeaseStore against a real
// *jobs.Store/*jobs.LeaseManager pair.
type leaseStoreAdapter struct {
	store *jobs.Store
	lease *jobs.LeaseManager
}

func (a leaseStoreAdapter) ListLeases(ctx context.Context, q rpc.LeaseListQuery) (rpc.LeasePage, error) {
	rows, cursor, err := a.store.ListLeases(ctx, jobs.LeaseFilter{ScopeGlob: q.ScopeGlob, Limit: q.Limit, Cursor: q.Cursor})
	if err != nil {
		return rpc.LeasePage{}, err
	}
	out := make([]rpc.LeaseRecord, 0, len(rows))
	for _, l := range rows {
		out = append(out, leaseRecordFrom(l))
	}
	return rpc.LeasePage{Leases: out, Cursor: cursor}, nil
}

func (a leaseStoreAdapter) GetLease(ctx context.Context, id string) (rpc.LeaseRecord, bool, error) {
	l, ok, err := a.store.GetLeaseByID(ctx, id)
	if err != nil || !ok {
		return rpc.LeaseRecord{}, ok, err
	}
	return leaseRecordFrom(l), true, nil
}

func (a leaseStoreAdapter) ReleaseLease(ctx context.Context, id string) error {
	l, ok, err := a.store.GetLeaseByID(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return a.lease.Release(ctx, l.RepoID, l.ScopeGlob, l.Epoch)
}

func leaseRecordFrom(l jobs.ResourceLease) rpc.LeaseRecord {
	return rpc.LeaseRecord{
		ID: jobs.LeaseID(l), RepoID: l.RepoID, ScopeGlob: l.ScopeGlob, Holder: l.Holder,
		IssuedAt: l.IssuedAt, TTLSeconds: l.TTLSeconds, RenewCount: l.RenewCount,
		Epoch: l.Epoch, State: string(l.State),
	}
}

// controllerGuardAdapter implements rpc.ControllerGuard against
// nodes.RequireController(role, method), enumerating
// nodes.GuardedMethods so the guarded method name is validated against
// the real production list.
type controllerGuardAdapter struct{ role nodes.Role }

func (a controllerGuardAdapter) RequireController(method string) error {
	return nodes.RequireController(a.role, method)
}

// outboxAdapter implements rpc.OutboxRecorder against the real
// jobs.DeriveIdempotencyKey/RecordIntent/ConfirmEffect over db — a
// second sqlite connection to the SAME cascade.db file wireJobRPC opened
// (see that file's doc comment for why a second connection is the
// established pattern here). Site is always jobs.OutboxSiteIntegration —
// see internal/rpc/jobs_effects.go's CONTRACT NOTE for why an RPC-
// triggered job/lease effect maps to that closed-vocabulary member.
type outboxAdapter struct {
	db    *sql.DB
	clock runtime.Clock
}

func (a outboxAdapter) DeriveKey(jobID string, attemptGeneration int64, payloadHash string) string {
	return jobs.DeriveIdempotencyKey(jobs.OutboxSiteIntegration, jobID, attemptGeneration, payloadHash)
}

func (a outboxAdapter) RecordIntent(ctx context.Context, jobID string, attemptGeneration int64, payloadHash string) error {
	return a.withTx(ctx, func(tx *sql.Tx, now int64) error {
		_, err := jobs.RecordIntent(ctx, tx, now, jobs.OutboxIntent{
			JobID: jobID, Site: jobs.OutboxSiteIntegration, AttemptGeneration: attemptGeneration, PayloadHash: payloadHash,
		})
		return err
	})
}

func (a outboxAdapter) Confirm(ctx context.Context, key string) error {
	return a.withTx(ctx, func(tx *sql.Tx, now int64) error {
		return jobs.ConfirmEffect(ctx, tx, now, key)
	})
}

func (a outboxAdapter) withTx(ctx context.Context, fn func(tx *sql.Tx, now int64) error) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx, a.clock.Now().Unix()); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
