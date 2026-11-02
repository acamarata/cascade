//go:build !windows

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ciAcceptanceVerifier runs acceptance on the daemon's recorded checkpoint.
// The dispatcher returns only after every terminal callback has completed.
type ciAcceptanceVerifier struct {
	dispatcher *ci.Dispatcher
	db         *sql.DB
	clock      runtime.Clock
	model      func(string) ci.RequirementModel
	terminal   chan struct{}
	ticker     func() runtime.Ticker
}

func newCIAcceptanceVerifier(w *daemonWiring) (*ciAcceptanceVerifier, error) {
	if w.Deps.CI == nil {
		return nil, cascade.New(cascade.KindUnavailable, "completion-gate: CI runtime unavailable")
	}
	v := &ciAcceptanceVerifier{
		dispatcher: w.Deps.CI.Dispatcher(), db: w.Deps.CI.CIDB(), clock: w.Clock,
		model: w.Deps.CI.Model, terminal: make(chan struct{}, 1),
		ticker: func() runtime.Ticker { return runtime.NewSystemTicker(time.Second) },
	}
	v.dispatcher.OnTerminal(func(ev ci.CIResultEvent) {
		if ev.Acceptance {
			select {
			case v.terminal <- struct{}{}:
			default:
			}
		}
	})
	return v, nil
}

// VerifyAcceptance waits for the full exact-checkpoint acceptance re-run.
func (v *ciAcceptanceVerifier) VerifyAcceptance(ctx context.Context, jobID string) (jobs.AcceptanceVerdict, error) {
	snap, found, err := v.dispatcher.CurrentCheckpoint(ctx, jobID)
	if err != nil || !found {
		return jobs.AcceptanceVerdict{Reason: "no current checkpoint"}, err
	}
	ref, err := v.checkpointRef(ctx, snap)
	if err != nil {
		return jobs.AcceptanceVerdict{}, err
	}
	return v.wait(ctx, func(ctx context.Context) (ci.AcceptanceResult, error) {
		return ciAcceptanceRerun(v.dispatcher, ctx, ref, v.model(ref.RepoRoot))
	})
}

// ciAcceptanceRerun is the dispatcher's exact-tree re-run. It is a package
// variable only so tests in a sandbox that forbids running git can stand in
// for the commit-tree resolution; production never reassigns it.
var ciAcceptanceRerun = (*ci.Dispatcher).AcceptanceRerun

func (v *ciAcceptanceVerifier) checkpointRef(ctx context.Context, snap ci.CandidateSnapshot) (ci.JobRef, error) {
	var raw string
	if err := v.db.QueryRowContext(ctx, `SELECT dispatches FROM ci_stream_attempt WHERE attempt_id = ?`, snap.AttemptID).Scan(&raw); err != nil {
		return ci.JobRef{}, cascade.Wrap(cascade.KindUnavailable, err, "completion-gate: read checkpoint dispatch")
	}
	var entries []struct {
		Ref ci.JobRef `json:"ref"`
	}
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return ci.JobRef{}, cascade.Wrap(cascade.KindIntegrity, err, "completion-gate: decode checkpoint dispatch")
	}
	if len(entries) == 0 {
		return ci.JobRef{}, cascade.New(cascade.KindUnavailable, "completion-gate: checkpoint has no dispatch")
	}
	return entries[0].Ref, nil
}

type ciAcceptanceOutcome struct {
	result ci.AcceptanceResult
	err    error
}

func (v *ciAcceptanceVerifier) wait(ctx context.Context, run func(context.Context) (ci.AcceptanceResult, error)) (jobs.AcceptanceVerdict, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	deadline := v.clock.Now().Add(30 * time.Minute)
	tick := v.ticker()
	defer tick.Stop()
	done := make(chan ciAcceptanceOutcome, 1)
	go func() { res, err := run(ctx); done <- ciAcceptanceOutcome{res, err} }()
	for {
		if !v.clock.Now().Before(deadline) {
			return jobs.AcceptanceVerdict{Reason: "acceptance timed out"}, nil
		}
		select {
		case <-ctx.Done():
			return jobs.AcceptanceVerdict{}, ctx.Err()
		case <-tick.C():
		case <-v.terminal:
			// A single terminal is only a wakeup; every kind must finish.
		case out := <-done:
			if !v.clock.Now().Before(deadline) {
				return jobs.AcceptanceVerdict{Reason: "acceptance timed out"}, nil
			}
			return jobs.AcceptanceVerdict{Passed: out.err == nil && out.result.Passed,
				CheckpointID: out.result.CheckpointID, TreeHash: out.result.TreeHash}, out.err
		}
	}
}
