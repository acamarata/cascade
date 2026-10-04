// Purpose: the fan-out primitive (R-21.214, BINDING, replaces the former
//   admission design): concurrent N-way model.execute dispatch. FanOut
//   calls no Admit and imports no governor package - the AO/S-79.T4
//   reservation pipeline is the sole caller of Admit for job dispatch
//   (R-21.64); the permit's lifetime is the adapter call only (R-21.122),
//   via the injected WithPermitFn seam. Every leg is journaled through the
//   injected JournalAppender and every successful leg's result is stored
//   through the injected LegResultStore (contract:fanout-leg-results), so
//   a resumed or re-attached fan-out replays finished legs instead of
//   paying for them twice - and only after re-authorizing them.
// Inputs: a per-call fan-out id, a provider.ModelRequest template, a leg
//   count n, a `completed` map (legs with an ok done entry), and the
//   injected seams (WithPermitFn, JournalAppender, LegResultStore,
//   AuthorizeFn) plus the exec function each leg dispatches through.
// Outputs: an index-ordered []provider.ModelResponse, or the first error
//   encountered (or ctx.Err() if ctx was cancelled).
// Constraints: every leg takes its own admission permit via withPermit and
//   releases it on every path; FanOut never returns while a launched
//   goroutine is still running. No credential and no model input or
//   output content reaches a journal entry - only the fan-out id, leg
//   index, attempt, outcome, result key, request digest and job id. Every
//   AppendLeg and LegResultStore error is returned, never discarded.
// SPORT: conductor.fanout/CHANGE (P1-E11-W3-S23-T2; P1-CORE-18).

package conductor

import (
	"context"
	"errors"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// WithPermitFn is the injected per-leg admission seam. In production the
// daemon composition root supplies a function that takes the reservation
// pipeline's permit immediately before fn runs and releases it when fn
// returns (R-21.122); in tests it is a pass-through or a denying double
// defined under _test.go only (Art.1).
type WithPermitFn func(ctx context.Context, fn func(context.Context) error) error

// JournalAppender is the injected journal seam a fan-out dispatch writes
// leg-lifecycle entries through (the fleet journal's KindFanOutLegStarted
// and KindFanOutLegDone). AppendLeg returns the leg attempt the entry was
// recorded under: for fanout_leg_started the journal allocates it (a
// durable attempt unique across every writer, at most three raw starts per
// leg) and refuses a leg whose start cap is spent; for
// fanout_leg_done fields["attempt"] names the start it closes. nil is a
// construction error, never a silent no-op.
type JournalAppender interface {
	AppendLeg(ctx context.Context, kind string, fanoutID string, legIndex int, fields map[string]string) (uint64, error)
}

// ErrAdmissionDenied is returned for a leg whose withPermit call itself
// failed (permit refused) before exec ever ran - never for an exec
// failure, which is reported unwrapped. It wraps KindQuotaExhausted, the
// same frozen kind internal/fleet/governor's own ErrThrottled uses for a
// resource-admission refusal (A-T7 sentinel rule; no second, competing
// kind is invented for the same concept).
var ErrAdmissionDenied = cascade.New(cascade.KindQuotaExhausted, "conductor: fan-out leg's admission permit was denied")

// legResult is one leg's outcome, collected off the buffered(n) channel.
type legResult struct {
	index int
	resp  provider.ModelResponse
	err   error
}

// execFn is the per-leg dispatch function FanOut calls (Executor.Execute
// in production).
type execFn func(context.Context, provider.ModelRequest) (provider.ModelResponse, error)

// fanOutRun holds one FanOut call's validated, shared collaborators.
type fanOutRun struct {
	fanoutID   string
	req        provider.ModelRequest
	completed  map[int]JobID
	withPermit WithPermitFn
	journal    JournalAppender
	results    LegResultStore
	authorize  AuthorizeFn
	exec       execFn
}

// FanOut dispatches n concurrent legs of req through exec, wrapped by
// withPermit, journaled through journal and persisted through results.
// n<0 fails closed to ErrInvalidRequest before any goroutine is launched;
// n==0 is treated as 1. A nil collaborator or an empty fanoutID is
// ErrConstructionFailed before any dispatch. A leg in completed, or with
// a stored result for this exact leg request, is replayed: authorize
// runs first and the stored Response is released only if it passes. On
// ctx cancellation, or if any leg errors, FanOut still drains every
// launched goroutine before returning, and returns no leg output.
func FanOut(
	ctx context.Context,
	fanoutID string,
	req provider.ModelRequest,
	n int,
	completed map[int]JobID,
	withPermit WithPermitFn,
	journal JournalAppender,
	results LegResultStore,
	authorize AuthorizeFn,
	exec func(context.Context, provider.ModelRequest) (provider.ModelResponse, error),
) ([]provider.ModelResponse, error) {
	if n < 0 {
		return nil, ErrInvalidRequest
	}
	if withPermit == nil || journal == nil || results == nil || authorize == nil || exec == nil {
		return nil, ErrConstructionFailed
	}
	if err := validateFanOutID(fanoutID); err != nil {
		return nil, err
	}
	if n == 0 {
		n = 1
	}
	run := &fanOutRun{fanoutID: fanoutID, req: req, completed: completed, withPermit: withPermit,
		journal: journal, results: results, authorize: authorize, exec: exec}
	return run.dispatchAll(ctx, n)
}

// dispatchAll launches every leg, collects every result, and returns the
// index-ordered responses only when no leg failed.
func (r *fanOutRun) dispatchAll(ctx context.Context, n int) ([]provider.ModelResponse, error) {
	ch := make(chan legResult, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ch <- r.dispatchLeg(ctx, idx)
		}(i)
	}

	results := make([]provider.ModelResponse, n)
	var firstErr error
	for i := 0; i < n; i++ {
		lr := <-ch
		if lr.err != nil && firstErr == nil {
			firstErr = lr.err
		}
		results[lr.index] = lr.resp
	}
	wg.Wait()

	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return results, nil
}

// dispatchLeg replays or runs exactly one leg and always returns a
// legResult - it never panics and never blocks past ctx or withPermit's
// own return.
func (r *fanOutRun) dispatchLeg(ctx context.Context, idx int) legResult {
	if err := ctx.Err(); err != nil {
		return legResult{index: idx, err: err}
	}
	legReq := r.req // per-leg copy
	// R-21.214: each leg resets its own FanOut to 1 (never the parent's
	// leg count) and clears ReservationID (a leg takes its own
	// reservation rather than inheriting the parent's). TaskID stays the
	// client's, so audit and usage attribution are unaffected.
	legReq.FanOut = 1
	legReq.ReservationID = ""

	digest, err := legRequestDigest(r.fanoutID, idx, legReq)
	if err != nil {
		return legResult{index: idx, err: err}
	}
	rec, found, err := r.results.GetLegResult(ctx, r.fanoutID, idx)
	if err != nil {
		return legResult{index: idx, err: err}
	}
	_, doneJournaled := r.completed[idx]
	if doneJournaled || found {
		return r.replayLeg(ctx, idx, legReq, digest, rec, found, doneJournaled)
	}
	return r.runLeg(ctx, idx, legReq, digest)
}

// runLeg dispatches one leg: started entry, permit-wrapped exec, then on
// success the LegResult (create-only) BEFORE the ok done entry; on
// failure a done entry carrying the failed outcome and no stored result.
func (r *fanOutRun) runLeg(ctx context.Context, idx int, legReq provider.ModelRequest, digest string) legResult {
	attempt, err := r.journal.AppendLeg(ctx, legKindStarted, r.fanoutID, idx, map[string]string{"request_digest": digest})
	if err != nil {
		return legResult{index: idx, err: err}
	}
	resp, legErr := r.execWithPermit(ctx, legReq)
	if legErr != nil {
		return r.failLeg(ctx, idx, attempt, digest, resp.JobID, legErr)
	}
	rec := LegResult{FanOutID: r.fanoutID, TaskID: legReq.TaskID, LegIndex: idx, Attempt: attempt,
		RequestDigest: digest, Sensitivity: ResolveSensitivity(legReq), Response: resp}
	if err := r.results.PutLegResult(ctx, rec); err != nil {
		return legResult{index: idx, err: err}
	}
	fields := doneFields(r.fanoutID, idx, attempt, LegOutcomeOK, digest, resp.JobID)
	if _, err := r.journal.AppendLeg(ctx, legKindDone, r.fanoutID, idx, fields); err != nil {
		return legResult{index: idx, err: err}
	}
	return legResult{index: idx, resp: resp}
}

// execWithPermit runs exec inside withPermit. A permit refused before
// exec ran is ErrAdmissionDenied; an exec failure is returned unwrapped.
func (r *fanOutRun) execWithPermit(ctx context.Context, legReq provider.ModelRequest) (provider.ModelResponse, error) {
	var resp provider.ModelResponse
	var execErr error
	dispatched := false
	permitErr := r.withPermit(ctx, func(pctx context.Context) error {
		dispatched = true
		resp, execErr = r.exec(pctx, legReq)
		return execErr
	})
	switch {
	case !dispatched && permitErr != nil:
		return resp, ErrAdmissionDenied
	case execErr != nil:
		return resp, execErr
	}
	return resp, nil
}

// failLeg journals a failed leg's done entry with its outcome and returns
// the leg error, joined with the append error when the append fails.
func (r *fanOutRun) failLeg(ctx context.Context, idx int, attempt uint64, digest string, jobID JobID, legErr error) legResult {
	fields := doneFields(r.fanoutID, idx, attempt, legOutcome(legErr), digest, jobID)
	if _, err := r.journal.AppendLeg(ctx, legKindDone, r.fanoutID, idx, fields); err != nil {
		return legResult{index: idx, err: errors.Join(legErr, err)}
	}
	return legResult{index: idx, err: legErr}
}

// ExecuteFanOutResponse dispatches n legs via Executor.ExecuteFanOut and
// assembles the R-21.214 fan-out parent-response shape: a fresh parent
// JobID, empty Output, Usage summed across every leg, and Legs holding
// each leg's own response in index order.
func (e *Executor) ExecuteFanOutResponse(ctx context.Context, fanoutID string, req provider.ModelRequest, n int, completed map[int]JobID, withPermit WithPermitFn, journal JournalAppender, results LegResultStore) (provider.ModelResponse, error) {
	legs, err := e.ExecuteFanOut(ctx, fanoutID, req, n, completed, withPermit, journal, results)
	if err != nil {
		return provider.ModelResponse{}, err
	}
	return assembleFanOutParent(legs)
}

// assembleFanOutParent builds the R-21.214 parent shape from an
// index-ordered leg slice: a fresh JobID, empty Output, Legs set verbatim,
// and Usage summed across every leg.
func assembleFanOutParent(legs []provider.ModelResponse) (provider.ModelResponse, error) {
	jobID, err := cascade.NewID()
	if err != nil {
		return provider.ModelResponse{}, cascade.Wrap(cascade.KindInternal, err, "conductor: minting fan-out parent job id")
	}
	parent := provider.ModelResponse{JobID: provider.JobID(jobID), Legs: legs}
	for _, leg := range legs {
		parent.Usage.InputTokens += leg.Usage.InputTokens
		parent.Usage.OutputTokens += leg.Usage.OutputTokens
	}
	return parent, nil
}
