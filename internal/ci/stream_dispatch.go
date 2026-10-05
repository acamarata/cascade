// Purpose (this file): Dispatch, the shared sub-job dispatch of Checkpoint
// and of P1-CI-02's acceptance re-run: one outbox intent and one sub-job
// per required kind, run in parallel through the SubJobExecutor, each
// result turned into a ci_run_stream marker, two events and an OnTerminal
// call, then confirmed in the outbox.
//
// Inputs: a JobRef, the CandidateSnapshot of a current attempt, the
// CIRequirementPlan whose Requirement names the kinds, and whether the
// dispatch is the acceptance re-run.
// Outputs: one CIResultEvent per kind (in requirement-kind order) and the
// joined errors of any kind that did not finish.
// Constraints: a tombstoned or unknown attempt is refused with
// ErrLateResult both before the sub-job starts and again before its result
// is recorded, and the lease epoch is presented again at both points (a
// reclaimed lease tombstones the attempt and records nothing); a late
// result changes no dispatcher row and publishes no terminal event. An acceptance dispatch refuses a stream-only snapshot
// (ErrTreeHashMismatch). The outbox order is intent -> sub-job (ci_run and
// ci_job committed by the executor) -> effect -> confirm, so a row is
// never confirmed before its rows exist. The same (attempt, kind) never
// runs twice at once (ErrAlreadyRunning).
// SPORT: internal.ci.Dispatcher.Dispatch/ADDED (P1-CI-01).

package ci

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
)

// streamEventSource is the events.Bus source of every dispatcher event.
const streamEventSource = "ci-stream"

// streamEventPayload is the wire payload of both event kinds.
type streamEventPayload struct {
	JobID        string `json:"job_id"`
	CheckpointID string `json:"checkpoint_id"`
	TreeHash     string `json:"tree_hash"`
	Kind         string `json:"kind"`
	Acceptance   bool   `json:"acceptance"`
	ViaStream    bool   `json:"via_stream"`
	Passed       bool   `json:"passed"`
	Sensitivity  string `json:"sensitivity"`
	RunID        int64  `json:"run_id"`
	RepoID       int64  `json:"repo_id"`
	ExecutorKind string `json:"executor_kind"`
}

// kindsOf lists the kinds plan requires, in the closed enum's order.
func kindsOf(plan CIRequirementPlan) []RequirementKind {
	var out []RequirementKind
	for _, k := range allRequirementKinds {
		if plan.Requirement.Requires(k) {
			out = append(out, k)
		}
	}
	return out
}

// Dispatch dispatches plan's requirement kinds as sub-jobs against snap.
func (d *Dispatcher) Dispatch(ctx context.Context, ref JobRef, snap CandidateSnapshot, plan CIRequirementPlan, acceptance bool) ([]CIResultEvent, error) {
	if err := d.requireCurrent(ctx, snap.AttemptID); err != nil {
		return nil, err
	}
	if acceptance && snap.StreamOnly() {
		return nil, cascade.Wrapf(cascade.KindIntegrity, ErrTreeHashMismatch,
			"ci: stream: snapshot %s carries declared untracked paths and cannot be used for acceptance", snap.CheckpointID)
	}
	e := dispatchEntry{Ref: ref, Snapshot: snap, Plan: plan, Acceptance: acceptance, Kinds: kindsOf(plan), Risk: plan.RiskClass}
	if err := d.appendEntry(ctx, snap.AttemptID, e); err != nil {
		return nil, err
	}
	if err := d.setState(ctx, snap.AttemptID, attemptTerminal, attemptLive); err != nil {
		return nil, err
	}
	if err := d.ensureIntents(ctx, e); err != nil {
		return nil, err
	}
	return d.runKinds(ctx, e)
}

// runKinds runs every kind of e in parallel and finishes the attempt when
// none is left unconfirmed.
func (d *Dispatcher) runKinds(ctx context.Context, e dispatchEntry) ([]CIResultEvent, error) {
	results := make([]CIResultEvent, len(e.Kinds))
	errs := make([]error, len(e.Kinds))
	var wg sync.WaitGroup
	for i, kind := range e.Kinds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = d.runKind(ctx, e, kind)
		}()
	}
	wg.Wait()
	var out []CIResultEvent
	for i, ev := range results {
		if errs[i] == nil {
			out = append(out, ev)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return out, err
	}
	return out, d.finishAttempt(ctx, e.Snapshot.AttemptID)
}

// runKind runs one kind: the shared body of Dispatch and Resume.
func (d *Dispatcher) runKind(ctx context.Context, e dispatchEntry, kind RequirementKind) (CIResultEvent, error) {
	key := outboxKeyFor(e, kind)
	if !d.begin(key) {
		return CIResultEvent{}, cascade.Wrapf(cascade.KindConflict, ErrAlreadyRunning, "ci: stream: %s is already running", key)
	}
	defer d.end(key)
	if err := d.requireCurrent(ctx, e.Snapshot.AttemptID); err != nil {
		return CIResultEvent{}, err
	}
	if err := d.refence(ctx, e); err != nil {
		return CIResultEvent{}, err
	}
	if err := d.publish(ctx, EventKindCheckpointDispatched, e, kind, SubJobResult{}); err != nil {
		return CIResultEvent{}, err
	}
	res, err := d.deps.Executor.Run(ctx, SubJob{Ref: e.Ref, Kind: kind, Acceptance: e.Acceptance, Snapshot: e.Snapshot, Plan: e.Plan})
	if err != nil {
		return CIResultEvent{}, err
	}
	if err := d.requireCurrent(ctx, e.Snapshot.AttemptID); err != nil {
		return CIResultEvent{}, err
	}
	if err := d.refence(ctx, e); err != nil {
		return CIResultEvent{}, err
	}
	return d.recordTerminal(ctx, e, kind, key, res)
}

// refence presents the dispatch's lease epoch again, before a sub-job
// starts and before its result is recorded: a lease reclaimed while the
// dispatcher was down or the sub-job ran must record nothing and fire no
// OnTerminal. A fenced attempt is tombstoned so Resume drops its rows.
func (d *Dispatcher) refence(ctx context.Context, e dispatchEntry) error {
	err := d.fence(ctx, e.Ref)
	if err != nil && errChainHas(err, jobs.ErrLeaseFenced) {
		if terr := d.tombstone(ctx, e.Snapshot.AttemptID); terr != nil {
			return errors.Join(err, terr)
		}
	}
	return err
}

// recordTerminal writes the ci_run_stream marker, publishes the terminal
// event, calls the OnTerminal callbacks, then marks effect and confirms.
func (d *Dispatcher) recordTerminal(ctx context.Context, e dispatchEntry, kind RequirementKind, key string, res SubJobResult) (CIResultEvent, error) {
	if err := UpsertRunSourceStream(ctx, d.deps.CIDB, res.RunID, res.RepoID, SourceLocal, true, e.Ref.Sensitivity, e.Snapshot.CheckpointID); err != nil {
		return CIResultEvent{}, err
	}
	ev := CIResultEvent{
		JobID: e.Ref.JobID, CheckpointID: e.Snapshot.CheckpointID, TreeHash: e.Snapshot.TreeHash, Kind: kind,
		Acceptance: e.Acceptance, Passed: res.Passed, RunID: res.RunID, RepoID: res.RepoID,
		ExecutorKind: res.ExecutorKind, NodeID: res.NodeID, Sensitivity: e.Ref.Sensitivity,
	}
	if err := d.publish(ctx, EventKindCheckpointTerminal, e, kind, res); err != nil {
		return ev, err
	}
	d.notifyTerminal(ev)
	if err := d.markEffect(ctx, key); err != nil {
		return ev, err
	}
	return ev, d.confirm(ctx, key)
}

// publish emits one dispatcher event on the ci_results namespace.
func (d *Dispatcher) publish(ctx context.Context, kind events.EventKind, e dispatchEntry, k RequirementKind, res SubJobResult) error {
	payload, err := json.Marshal(streamEventPayload{
		JobID: e.Ref.JobID, CheckpointID: e.Snapshot.CheckpointID, TreeHash: e.Snapshot.TreeHash, Kind: string(k),
		Acceptance: e.Acceptance, ViaStream: true, Passed: res.Passed, Sensitivity: e.Ref.Sensitivity.String(),
		RunID: res.RunID, RepoID: res.RepoID, ExecutorKind: res.ExecutorKind,
	})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "ci: stream: encode event payload")
	}
	if _, err := d.deps.Bus.Publish(ctx, EventNamespace, kind, streamEventSource, payload); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "ci: stream: publish event")
	}
	return nil
}

// notifyTerminal calls every OnTerminal callback, serialised.
func (d *Dispatcher) notifyTerminal(ev CIResultEvent) {
	d.cbMu.Lock()
	defer d.cbMu.Unlock()
	for _, fn := range d.terminals {
		fn(ev)
	}
}

// begin claims key for this process; false means it is already running.
func (d *Dispatcher) begin(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, busy := d.inflight[key]; busy {
		return false
	}
	d.inflight[key] = struct{}{}
	return true
}

func (d *Dispatcher) end(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.inflight, key)
}

// finishAttempt moves a live attempt to terminal once none of its outbox
// keys is left unconfirmed.
func (d *Dispatcher) finishAttempt(ctx context.Context, attemptID string) error {
	att, ok, err := d.getAttempt(ctx, attemptID)
	if err != nil || !ok || att.State != attemptLive {
		return err
	}
	pending, err := d.unconfirmedKeys(ctx)
	if err != nil {
		return err
	}
	for _, e := range att.Entries {
		for _, kind := range e.Kinds {
			if _, open := pending[outboxKeyFor(e, kind)]; open {
				return nil
			}
		}
	}
	return d.setState(ctx, attemptID, attemptLive, attemptTerminal)
}
