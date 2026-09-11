// Purpose: the fan-out primitive (R-21.214, BINDING, replaces the former
//   admission design): concurrent N-way model.execute dispatch. FanOut
//   calls no Admit and imports no governor package - the AO/S-79.T4
//   reservation pipeline is the sole caller of Admit for job dispatch
//   (R-21.64); the permit's lifetime is the adapter call only (R-21.122),
//   via the injected WithPermitFn seam. Every leg is journaled through the
//   injected JournalAppender seam so a resumed dispatch can skip legs that
//   already finished.
// Inputs: a provider.ModelRequest template, a leg count n, a resume
//   `completed` map, and the two injected seams (WithPermitFn,
//   JournalAppender) plus the exec function each leg dispatches through.
// Outputs: an index-ordered []provider.ModelResponse, or the first error
//   encountered (or ctx.Err() if ctx was cancelled).
// Constraints: every leg takes its own admission permit via withPermit and
//   releases it on every path (success, error, cancellation); FanOut never
//   returns while a launched goroutine is still running. No credential, no
//   model input or output content reaches a journal entry - only task_id,
//   leg_index, a request digest and a job id.
// SPORT: conductor.fanout/ADD (P1-E11-W3-S23-T2).

package conductor

import (
	"context"
	"strconv"
	"sync"

	"github.com/acamarata/cascade/internal/audit"
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
// leg-lifecycle entries through. It is satisfied at the daemon composition
// root by the fleet journal store's Kind{FanOutLegStarted,FanOutLegDone}
// entries; nil is a construction error, never a silent no-op.
type JournalAppender interface {
	AppendLeg(ctx context.Context, kind string, taskID string, legIndex int, fields map[string]string) error
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

// FanOut dispatches n concurrent legs of req through exec, wrapped by
// withPermit and journaled through journal. n<0 fails closed to
// ErrInvalidRequest before any goroutine is launched; n==0 is treated as
// 1. A leg whose index is present in completed is skipped entirely (no
// permit, no exec, no journal entry) and its response is replayed with
// only the JobID this ticket has available - completed[i] - since the
// jobs-domain usage-row lookup that would supply the full replayed
// response belongs to a not-yet-built ticket (S-23.T4); see this
// package's journal for both sides quoted. On ctx cancellation, or if any
// leg errors, FanOut still drains every launched goroutine before
// returning.
func FanOut(
	ctx context.Context,
	req provider.ModelRequest,
	n int,
	completed map[int]JobID,
	withPermit WithPermitFn,
	journal JournalAppender,
	exec func(context.Context, provider.ModelRequest) (provider.ModelResponse, error),
) ([]provider.ModelResponse, error) {
	if n < 0 {
		return nil, ErrInvalidRequest
	}
	if withPermit == nil || journal == nil || exec == nil {
		return nil, ErrConstructionFailed
	}
	if n == 0 {
		n = 1
	}

	ch := make(chan legResult, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ch <- dispatchLeg(ctx, req, idx, completed, withPermit, journal, exec)
		}(i)
	}

	results := make([]provider.ModelResponse, n)
	var firstErr error
	for i := 0; i < n; i++ {
		r := <-ch
		if r.err != nil && firstErr == nil {
			firstErr = r.err
		}
		results[r.index] = r.resp
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

// dispatchLeg runs (or replays, or skips) exactly one leg and always
// returns a legResult - it never panics and never blocks past ctx or
// withPermit's own return.
func dispatchLeg(
	ctx context.Context,
	req provider.ModelRequest,
	idx int,
	completed map[int]JobID,
	withPermit WithPermitFn,
	journal JournalAppender,
	exec func(context.Context, provider.ModelRequest) (provider.ModelResponse, error),
) legResult {
	if jobID, ok := completed[idx]; ok {
		return legResult{index: idx, resp: provider.ModelResponse{JobID: jobID}}
	}
	if err := ctx.Err(); err != nil {
		return legResult{index: idx, err: err}
	}

	legReq := req // per-leg copy; FanOut/ReservationID clearing is not
	// possible here - provider.ModelRequest carries neither field and
	// pkg/provider/model.go is outside this ticket's files_scope. See the
	// journal's contradiction entry (mirrors S-22.T1's own inherited
	// blocker).

	digest := audit.HashParams([]byte(legReq.TaskID + "|" + strconv.Itoa(idx)))
	_ = journal.AppendLeg(ctx, "fanout_leg_started", legReq.TaskID, idx, map[string]string{
		"request_digest": digest,
	})

	var resp provider.ModelResponse
	var execErr error
	dispatched := false
	permitErr := withPermit(ctx, func(pctx context.Context) error {
		dispatched = true
		resp, execErr = exec(pctx, legReq)
		return execErr
	})

	var legErr error
	switch {
	case !dispatched && permitErr != nil:
		legErr = ErrAdmissionDenied
	case execErr != nil:
		legErr = execErr
	}

	_ = journal.AppendLeg(ctx, "fanout_leg_done", legReq.TaskID, idx, map[string]string{
		"job_id": string(resp.JobID),
	})

	return legResult{index: idx, resp: resp, err: legErr}
}
