package daemon

// Purpose: the durable fan-out branch of "conductor.execute"
//   (contract:fanout-producer). A call with 2 <= fan_out <= MaxFanOut is
//   authorized on the parent request before any write, keyed by the
//   client's request_id or a minted id, claimed in the per-daemon table,
//   and then either STARTED (cursor, request record, legs, delivery, final
//   marker, deletes) or RE-ATTACHED (done-ok legs replayed after
//   authorization, missing legs dispatched, delivered to this caller).
// Inputs: the one production *conductor.Executor and ConductorFanOut (the
//   fleet journal head reader, the resume.FanOutStore over the daemon
//   store, the leg budget, the claim table and the id source).
// Outputs: the fan-out parent response plus the echoed request_id.
// Constraints: every leg goes through Executor.ExecuteFanOutResponse (the
//   executor's own door checks); no request content reaches the journal;
//   every return after the cursor finalizes under context.WithoutCancel
//   with a 5s bound, except the concurrent-start (seq != 1) refusal. The
//   request_id never reaches a log or an event.
// SPORT: internal/daemon (CHANGE, P1-CORE-19).

import (
	"context"
	"errors"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/fleet/resume"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// fanOutFinalizeBound caps the detached finalize of one fan-out call.
const fanOutFinalizeBound = 5 * time.Second

// ErrFanOutInProgress refuses a call whose fan-out id is already claimed
// by a live call, a re-attach, a scan or a sweep.
var ErrFanOutInProgress = cascade.New(cascade.KindConflict, "conductor.execute: fan-out in progress")

// ErrFanOutNotFound refuses a re-attach whose entity is not one of ours.
var ErrFanOutNotFound = cascade.New(cascade.KindNotFound, "conductor.execute: no fan-out with this request_id")

// ConductorFanOut carries the durable fan-out collaborators the
// composition root builds over the daemon store. All four seams are
// required; Err records a failed construction. IDSource defaults to
// cascade.NewID.
type ConductorFanOut struct {
	Journal  resume.HeadReader
	Results  *resume.FanOutStore
	Budget   conductor.WithPermitFn
	Claims   *resume.Claims
	IDSource func() (cascade.ID, error)
	Err      error
}

// NewConductorFanOut builds the production ConductorFanOut over the
// daemon store: the fleet journal (DefaultNamespace), the FanOutStore, a
// DefaultFanOutLegBudget-slot budget and a fresh claim table. A nil store
// is recorded in Err, so fan-out refuses while fan_out <= 1 still serves.
func NewConductorFanOut(store provider.Store, clock runtime.Clock) ConductorFanOut {
	if store == nil || clock == nil {
		return ConductorFanOut{Err: cascade.New(cascade.KindUnavailable, "fan-out needs the daemon store and clock")}
	}
	js := journal.New(store, clock, journal.DefaultNamespace)
	results, err := resume.NewFanOutStore(js, store)
	return ConductorFanOut{Journal: js, Results: results, Err: err,
		Budget: conductor.NewLegBudget(conductor.DefaultFanOutLegBudget), Claims: resume.NewClaims()}
}

// unwired names the first missing collaborator, or returns nil.
func (f ConductorFanOut) unwired() error {
	switch {
	case f.Err != nil:
		return f.Err
	case f.Journal == nil:
		return cascade.New(cascade.KindUnavailable, "fan-out journal not wired")
	case f.Results == nil:
		return cascade.New(cascade.KindUnavailable, "fan-out results store not wired")
	case f.Budget == nil:
		return cascade.New(cascade.KindUnavailable, "fan-out leg budget not wired")
	case f.Claims == nil:
		return cascade.New(cascade.KindUnavailable, "fan-out claim table not wired")
	}
	return nil
}

// summary is the manifest clause saying whether durable fan-out serves.
func (f ConductorFanOut) summary() string {
	if err := f.unwired(); err != nil {
		return "fan-out unavailable (" + err.Error() + ")"
	}
	return "durable fan-out wired"
}

// fanOutResponse is the fan-out parent response plus the echoed id.
type fanOutResponse struct {
	provider.ModelResponse
	RequestID string `json:"request_id"`
}

// fanOutRunner serves one daemon's fan-out calls.
type fanOutRunner struct {
	exec *conductor.Executor
	cfg  ConductorFanOut
}

// serve runs one validated fan-out call (see the file comment).
func (r fanOutRunner) serve(ctx context.Context, requestID string, req provider.ModelRequest) (any, error) {
	if err := r.cfg.unwired(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "conductor.execute: fan-out unavailable")
	}
	parent := req
	parent.FanOut = 1
	if err := r.exec.Authorize(ctx, parent); err != nil {
		return nil, err
	}
	id, minted, err := r.fanOutID(requestID)
	if err != nil {
		return nil, err
	}
	if !r.cfg.Claims.TryClaim(id) {
		return nil, ErrFanOutInProgress
	}
	defer r.cfg.Claims.Release(id)
	head, err := r.cfg.Journal.HeadSeq(ctx, resume.FanOutEntity(id))
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "conductor.execute: reading the fan-out journal")
	}
	var resp provider.ModelResponse
	switch {
	case head == 0:
		resp, err = r.start(ctx, id, req)
	case minted:
		return nil, cascade.New(cascade.KindInternal, "conductor.execute: a freshly minted fan-out id already has journal entries")
	default:
		resp, err = r.reattach(ctx, id, req)
	}
	if err != nil {
		return nil, err
	}
	return fanOutResponse{ModelResponse: resp, RequestID: id}, nil
}

// fanOutID returns the client's request_id (already validated) or a
// minted id; minted reports which.
func (r fanOutRunner) fanOutID(requestID string) (string, bool, error) {
	if requestID != "" {
		return requestID, false, nil
	}
	mint := r.cfg.IDSource
	if mint == nil {
		mint = cascade.NewID
	}
	id, err := mint()
	if err != nil {
		return "", false, cascade.Wrap(cascade.KindInternal, err, "conductor.execute: minting a fan-out id")
	}
	return string(id), true, nil
}

// start runs a new fan-out: cursor (intent), request record, legs,
// delivery and finalize.
func (r fanOutRunner) start(ctx context.Context, id string, req provider.ModelRequest) (provider.ModelResponse, error) {
	digest, err := resume.RequestDigest(req)
	if err != nil {
		return provider.ModelResponse{}, err
	}
	seq, err := r.cfg.Results.WriteCursor(ctx, resume.FanOutCursor{FanOutID: id, TaskID: req.TaskID,
		Legs: req.FanOut, RequestDigest: digest, RequestKey: id})
	if err != nil {
		return provider.ModelResponse{}, cascade.Wrap(cascade.KindUnavailable, err, "conductor.execute: writing the fan-out cursor")
	}
	if seq != 1 {
		return provider.ModelResponse{}, cascade.New(cascade.KindConflict, "conductor.execute: concurrent fan-out start")
	}
	if err := r.cfg.Results.PutRequest(ctx, id, req); err != nil {
		return r.finish(ctx, id, provider.ModelResponse{}, err, resume.OutcomeTerminal)
	}
	resp, err := r.exec.ExecuteFanOutResponse(ctx, id, req, req.FanOut, nil, r.cfg.Budget, r.cfg.Results, r.cfg.Results)
	return r.finish(ctx, id, resp, err, fanOutOutcome(ctx, err))
}

// reattach resumes an existing fan-out for this caller.
func (r fanOutRunner) reattach(ctx context.Context, id string, req provider.ModelRequest) (provider.ModelResponse, error) {
	st, err := r.cfg.Results.State(ctx, id)
	if err != nil {
		return provider.ModelResponse{}, cascade.Wrap(cascade.KindUnavailable, err, "conductor.execute: reading the fan-out journal")
	}
	if !st.Ours {
		return provider.ModelResponse{}, ErrFanOutNotFound
	}
	if st.Final != "" {
		return provider.ModelResponse{}, cascade.Newf(cascade.KindConflict, "conductor.execute: fan-out already finalized: %s", st.Final)
	}
	stored, found, err := r.cfg.Results.GetRequest(ctx, id)
	if err != nil {
		return provider.ModelResponse{}, err
	}
	if !found {
		return r.finish(ctx, id, provider.ModelResponse{}, resume.ErrRequestRecordMissing, resume.OutcomeTerminal)
	}
	if digest, err := resume.RequestDigest(req); err != nil || digest != st.Cursor.RequestDigest {
		return provider.ModelResponse{}, errors.Join(err, cascade.New(cascade.KindConflict, "conductor.execute: request differs from the fan-out's stored request"))
	}
	if st.Terminal {
		return r.finish(ctx, id, provider.ModelResponse{}, resume.ErrLegTerminal, resume.OutcomeTerminal)
	}
	for leg := 0; leg < st.Cursor.Legs; leg++ {
		if _, ok := st.Completed[leg]; !ok && st.Starts[leg] >= resume.MaxLegStarts {
			return r.finish(ctx, id, provider.ModelResponse{}, resume.ErrLegAttemptsExhausted, resume.OutcomeUnknown)
		}
	}
	resp, err := r.exec.ExecuteFanOutResponse(ctx, id, stored, st.Cursor.Legs, st.Completed, r.cfg.Budget, r.cfg.Results, r.cfg.Results)
	return r.finish(ctx, id, resp, err, fanOutOutcome(ctx, err))
}

// finish finalizes id with outcome under a detached, bounded context and
// returns resp only when neither cause nor the finalize failed. A marker
// error never skips the deletes (FanOutStore.Finalize); every error is
// joined into the one returned.
func (r fanOutRunner) finish(ctx context.Context, id string, resp provider.ModelResponse, cause error, outcome string) (provider.ModelResponse, error) {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fanOutFinalizeBound)
	defer cancel()
	ferr := r.cfg.Results.Finalize(fctx, id, outcome)
	if ferr != nil {
		ferr = cascade.Wrap(cascade.KindUnavailable, ferr, "conductor.execute: finalizing fan-out ("+outcome+")")
	}
	if err := errors.Join(cause, ferr); err != nil {
		return provider.ModelResponse{}, err
	}
	return resp, nil
}

// fanOutOutcome maps a dispatch result onto the outcome table.
func fanOutOutcome(ctx context.Context, err error) string {
	switch {
	case err == nil:
		return resume.OutcomeDelivered
	case errors.Is(ctx.Err(), context.Canceled):
		return resume.OutcomeCancelled
	case errors.Is(ctx.Err(), context.DeadlineExceeded), holds(err, resume.ErrLegAttemptsExhausted):
		return resume.OutcomeUnknown
	case cascade.HasKind(err, cascade.KindPolicyDenied), cascade.HasKind(err, cascade.KindInvalidInput):
		return resume.OutcomeTerminal
	default:
		return resume.OutcomeFailed
	}
}

// holds reports whether target itself (by identity, not by Kind) is in
// err's chain, including joined errors.
func holds(err, target error) bool {
	if err == nil {
		return false
	}
	if err == target { //nolint:errorlint // identity is the point: errors.Is compares Kind only
		return true
	}
	switch u := err.(type) { //nolint:errorlint // walking the chain by hand
	case interface{ Unwrap() []error }:
		for _, e := range u.Unwrap() {
			if holds(e, target) {
				return true
			}
		}
	case interface{ Unwrap() error }:
		return holds(u.Unwrap(), target)
	}
	return false
}
