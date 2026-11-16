//go:build !windows

// Purpose: gate-stall actions for the daemon's stall detector.
// When a completion gate exhausts its retry bound the detector reads the
// last denial, fails the job, releases its reservation through
// nodes.CancelInFlight (its leases follow through the terminal hook) and
// files the retry hint from jobs.GateAttemptPolicy.
// Constraints: <=300 lines, <=50/func, 14-kind errors, injected clock only.
// SPORT: cmd/cascade daemon gate-stall actions (ADDED, P1-PBD-04d).
package main

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// gateStall holds what the actions read: the runtime's one jobs handle and
// the first time each (job, attempt) denial was observed, so a replayed
// stall carries the same DeniedAt.
type gateStall struct {
	rt    *daemon.FleetRuntime
	clock runtime.Clock
	mu    sync.Mutex
	seen  map[string]time.Time
}

// gateStallActions returns the production GateStallActions over rt.
func gateStallActions(rt *daemon.FleetRuntime, clock runtime.Clock) supervision.GateStallActions {
	g := &gateStall{rt: rt, clock: clock, seen: map[string]time.Time{}}
	return supervision.GateStallActions{
		LastDenial: g.lastDenial,
		FailJob:    g.failJob,
		Releases:   g.releases,
		RetryHint:  func(attempt int) (time.Duration, error) { return jobs.GateAttemptPolicy{}.Delay(attempt) },
	}
}

// wireGateStallActions makes sure the attempts table exists and gives det
// its gate-stall actions.
func wireGateStallActions(ctx context.Context, det *supervision.Detector, rt *daemon.FleetRuntime, clock runtime.Clock) error {
	if err := jobs.ApplyGateAttemptsSchema(ctx, rt.JobsDB(), migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		return err
	}
	return det.WithGateStallActions(gateStallActions(rt, clock))
}

// lastDenial reads the recorded attempt and reason for the job's latest
// execution. A missing row is an error, never attempt 0.
func (g *gateStall) lastDenial(ctx context.Context, jobID string) (supervision.GateDenial, error) {
	var d supervision.GateDenial
	err := g.rt.JobsDB().QueryRowContext(ctx,
		`SELECT g.attempts, g.reason FROM jobs_gate_attempts g
		 JOIN jobs_execution e ON e.id = g.execution_id
		 WHERE e.job_id = ? ORDER BY e.attempt DESC LIMIT 1`, jobID).Scan(&d.Attempt, &d.Reason)
	if err == sql.ErrNoRows {
		return supervision.GateDenial{}, cascade.Newf(cascade.KindNotFound, "gate stall: no denial recorded for job %q", jobID)
	}
	if err != nil {
		return supervision.GateDenial{}, cascade.Wrap(cascade.KindUnavailable, err, "gate stall: read last denial")
	}
	key := fmt.Sprintf("%s|%d", jobID, d.Attempt)
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.seen[key]; !ok {
		g.seen[key] = g.clock.Now()
	}
	d.DeniedAt = g.seen[key]
	return d, nil
}

// failJob moves the job to failed and records the exhausted-gate result. It
// refuses KindConflict unless the recorded attempt has reached the bound.
func (g *gateStall) failJob(ctx context.Context, jobID string, d supervision.GateDenial) error {
	if !(jobs.GateAttemptPolicy{}).ShouldStall(d.Attempt) {
		return cascade.Newf(cascade.KindConflict, "gate stall: attempt %d is below the stall bound", d.Attempt)
	}
	store := g.rt.Jobs()
	exec, ok, err := store.LatestExecutionForJob(ctx, jobID)
	if err != nil {
		return err
	}
	if !ok {
		return cascade.Newf(cascade.KindNotFound, "gate stall: job %q has no execution", jobID)
	}
	if err := store.PutTransition(ctx, jobID, jobs.JobStateFailed, g.clock.Now().Unix()); err != nil {
		return err
	}
	return store.PutExecutionResult(ctx, jobs.ExecutionResult{
		ExecutionID:  exec.ID,
		ErrorMessage: "gate_attempts_exhausted: " + d.Reason,
	})
}

// releases returns one release for the latest execution's reservation, or
// none when no live reservation is held. A reservation that is not the
// job's own is refused.
func (g *gateStall) releases(ctx context.Context, jobID string) ([]nodes.ScopeRelease, error) {
	exec, ok, err := g.rt.Jobs().LatestExecutionForJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	rsv, found, err := g.rt.Fleet().ReservationByExecution(ctx, exec.ID)
	if err != nil {
		return nil, err
	}
	if !found || rsv.State.Terminal() {
		return nil, nil
	}
	if rsv.JobID != jobID {
		return nil, cascade.Newf(cascade.KindConflict, "gate stall: reservation %q belongs to another job", rsv.ID)
	}
	reserver := g.rt.Reserver()
	return []nodes.ScopeRelease{func() error {
		_, err := reserver.Release(ctx, rsv.ID)
		return err
	}}, nil
}
