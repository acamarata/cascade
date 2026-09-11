// Purpose: the two entrypoints of the model.execute door - Execute
//   (blocking) and ExecuteStream (streaming) - and the thin ordered-call
//   sequencing R-21.206 requires: readiness gate, validation, classifier,
//   sensitivity/task-class resolution, policy, router selection, dispatch
//   with the single R-21.213 capability-denied failover, the one-terminal-
//   event audit write, and A-T7 error mapping.
// Inputs: a provider.ModelRequest.
// Outputs: a provider.ModelResponse (Execute) or a buffered StreamEvent
//   channel plus CancelFunc (ExecuteStream), or a taxonomy error.
// Constraints: execute.go stays a thin orchestrator - every later concern
//   (egress substitution, fan-out, usage attribution) belongs in its own
//   file. Exactly one audit record per terminal outcome (R-14.37); zero
//   provider calls before Ready() passes.
// SPORT: conductor.execute/ADD (P1-E11-W3-S22-T1).

package conductor

import (
	"context"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Executor implements provider.ModelExecutor: the sole caller-facing model
// call door. Build one with NewExecutor; the zero value is not usable.
//
// cancelsOnce/cancelsRegistry/bridge (P1-E11-W3-S23-T3) are the streaming
// and cancellation collaborators: a lazily-constructed cancel-map (see
// stream.go's cancels() getter) and an optional SSE publish seam (see
// SetEventBridge). Both are added as struct fields here, rather than
// ExecutorConfig parameters, because pipeline.go's ExecutorConfig and
// NewExecutor are outside this ticket's files_scope - see stream.go's
// journal note for both sides quoted. Every existing NewExecutor call
// site keeps compiling and working unchanged: cancelsRegistry comes into
// being on first use, and a nil bridge makes every publish a documented
// no-op.
// spawnHook/spawnSem/spawnDrops (P1-E12-W3-S25-T3) are the unified
// spawned-agent registration seam - see spawn_hook.go for SetSpawnHook,
// trySpawn/enqueueSpawn and this ticket's CONTRACT DEVIATION note. Added
// as struct fields for the same reason cancelsOnce/cancelsRegistry/bridge
// were: ExecutorConfig/NewExecutor (pipeline.go) are outside this ticket's
// files_scope too.
type Executor struct {
	pipeline        *Pipeline
	router          Router
	cancelsOnce     sync.Once
	cancelsRegistry *cancelRegistry
	bridge          EventBridge
	spawnHook       SpawnHook
	spawnSem        chan struct{}
	spawnDrops      int64
}

// CancelFunc cancels an in-flight ExecuteStream job. It is idempotent
// (safe to call more than once) and never panics on an already-closed
// stream channel. It cancels the job's context only; the wire-level
// job.cancel JSON-RPC bridge over POST /rpc is S-23.T3's (R-40.X9).
type CancelFunc func()

var _ provider.ModelExecutor = (*Executor)(nil)

// Execute runs req to completion through the sole model.execute door.
//
// P1-E11-W3-S23-T3 extends this method with the one-terminal-event rule
// (R-21.217): every dispatched-past-authorization call mints a JobID,
// registers a cancel func for it BEFORE dispatch, and publishes exactly
// one terminal SSE event (done|error) through the bridge before
// deregistering - so a GET /events?filter=job:<id> subscriber to a
// one-shot job never hangs waiting for a terminal event
// (TestStream_OneShotTerminalEventEmitted), and job.cancel on a one-shot
// job in flight actually cancels its own dispatch context
// (TestStream_OneShotJobID). A request that never reaches dispatch
// (authorize failure) mints no job id at all - there is nothing yet for a
// client to subscribe to.
func (e *Executor) Execute(ctx context.Context, req provider.ModelRequest) (provider.ModelResponse, error) {
	if err := e.pipeline.Ready(); err != nil {
		return provider.ModelResponse{}, err
	}
	tier, err := e.authorize(ctx, req)
	if err != nil {
		e.auditRefusal(ctx, req, tier, err)
		return provider.ModelResponse{}, err
	}

	rawID, idErr := cascade.NewID()
	if idErr != nil {
		return provider.ModelResponse{}, cascade.Wrap(cascade.KindInternal, idErr, "conductor: minting job id")
	}
	jobID := JobID(rawID)
	dctx, cancel := context.WithCancel(ctx)
	e.cancels().register(jobID, cancel)
	defer func() {
		cancel()
		e.cancels().deregister(jobID)
	}()

	sel, err := e.router.Select(dctx, req)
	if err != nil {
		e.auditRefusal(ctx, req, tier, ErrNoLane)
		e.publishTerminal(ctx, jobID, "error")
		return provider.ModelResponse{}, ErrNoLane
	}
	req, err = e.substituteInputs(dctx, req, tier)
	if err != nil {
		e.auditOutcome(ctx, req, tier, sel, "egress_substitution_failed", err)
		e.publishTerminal(ctx, jobID, "error")
		return provider.ModelResponse{}, ErrEgressSubstitutionFailed
	}
	resp, finalSel, err := e.dispatchWithFailover(dctx, sel, req)
	if err != nil {
		mapped := mapProviderError(err)
		e.auditOutcome(ctx, req, tier, finalSel, "provider_failure", mapped)
		e.publishTerminal(ctx, jobID, "error")
		return provider.ModelResponse{}, mapped
	}
	out := provider.ModelResponse{
		JobID:     provider.JobID(jobID),
		Selection: finalSel,
		Output:    resp.Message.Content,
		Usage:     resp.Usage,
	}
	e.auditOutcome(ctx, req, tier, finalSel, "success", nil)
	e.publishTerminal(ctx, jobID, "done")
	e.trySpawn(req, finalSel) // P1-E12-W3-S25-T3: no-op unless IsSubprocessDispatch(finalSel)
	return out, nil
}

// authorize runs validation, the classifier, sensitivity/task-class
// resolution and policy, in that order. Its own error is always
// ErrInvalidRequest or a KindPolicyDenied wrap; the caller audits and
// returns it unchanged.
func (e *Executor) authorize(ctx context.Context, req provider.ModelRequest) (provider.SensitivityTier, error) {
	tier := ResolveSensitivity(req)
	if err := validateRequest(req); err != nil {
		return tier, ErrInvalidRequest
	}
	if err := e.pipeline.cfg.Classifier.Classify(ctx, req); err != nil {
		return tier, ErrInvalidRequest
	}
	if _, err := ResolveTaskClass(req); err != nil {
		return tier, ErrInvalidRequest
	}
	tier = e.pipeline.cfg.Sensitivity.Resolve(tier)
	if tier == provider.SensitivityLocalOnly && req.Policy.ExternalAllowed {
		return tier, ErrSensitivityViolation
	}
	if err := e.pipeline.cfg.Policy.Authorize(ctx, req); err != nil {
		return tier, cascade.Wrap(cascade.KindPolicyDenied, err, "conductor: policy refused")
	}
	return tier, nil
}

// dispatchWithFailover dispatches to sel, and on a capability-denied
// provider error re-selects EXACTLY ONCE with sel excluded, then dispatches
// to the re-selected lane. Never a retry on the denying lane, never
// rotation, never a second failover (R-21.213).
func (e *Executor) dispatchWithFailover(ctx context.Context, sel provider.Selection, req provider.ModelRequest) (provider.ChatResponse, provider.Selection, error) {
	dispatch := e.pipeline.capability()
	chatReq := toChatRequest(req)
	resp, err := dispatch(ctx, sel, chatReq)
	if err == nil || !cascade.HasKind(err, cascade.KindCapabilityDenied) {
		return resp, sel, err
	}
	reSel, selErr := e.router.Select(ctx, req, sel.LaneID)
	if selErr != nil {
		return provider.ChatResponse{}, sel, err
	}
	resp, err = dispatch(ctx, reSel, chatReq)
	return resp, reSel, err
}

// ExecuteStream runs req as a streaming exchange: a buffered(1) channel of
// StreamEvent, closed when the provider finishes or CancelFunc is called.
func (e *Executor) ExecuteStream(ctx context.Context, req provider.ModelRequest) (<-chan provider.StreamEvent, CancelFunc, error) {
	if err := e.pipeline.Ready(); err != nil {
		return nil, nil, err
	}
	tier, err := e.authorize(ctx, req)
	if err != nil {
		e.auditRefusal(ctx, req, tier, err)
		return nil, nil, err
	}
	sel, err := e.router.Select(ctx, req)
	if err != nil {
		e.auditRefusal(ctx, req, tier, ErrNoLane)
		return nil, nil, ErrNoLane
	}
	req, err = e.substituteInputs(ctx, req, tier)
	if err != nil {
		e.auditOutcome(ctx, req, tier, sel, "egress_substitution_failed", err)
		return nil, nil, ErrEgressSubstitutionFailed
	}
	ch := make(chan provider.StreamEvent, 1)
	cctx, cancel := context.WithCancel(ctx)
	var once sync.Once
	cancelFn := CancelFunc(func() {
		once.Do(func() {
			cancel()
			go drain(ch)
		})
	})
	go e.runStream(cctx, req, sel, tier, ch)
	return ch, cancelFn, nil
}

// runStream drives one streaming dispatch to completion and writes exactly
// one terminal audit record (success or cancellation/provider failure).
func (e *Executor) runStream(ctx context.Context, req provider.ModelRequest, sel provider.Selection, tier provider.SensitivityTier, ch chan<- provider.StreamEvent) {
	defer close(ch)
	driver, err := e.pipeline.cfg.Resolver.Resolve(ctx, sel)
	if err != nil {
		e.auditOutcome(context.WithoutCancel(ctx), req, tier, sel, "provider_failure", err)
		_ = send(ctx, ch, provider.StreamEvent{Kind: provider.StreamEventError, Err: err})
		return
	}
	chatReq := toChatRequest(req)
	sinkErr := driver.Stream(ctx, chatReq, func(ev provider.StreamEvent) error {
		return send(ctx, ch, ev)
	})
	outcome := "success"
	if sinkErr != nil {
		outcome = "provider_failure"
		if ctx.Err() != nil {
			outcome = "cancellation"
		}
	}
	e.auditOutcome(context.WithoutCancel(ctx), req, tier, sel, outcome, sinkErr)
}

// send delivers ev to ch, or returns ctx.Err() if ctx is done first, so a
// cancelled stream never blocks the driver goroutine forever.
func send(ctx context.Context, ch chan<- provider.StreamEvent, ev provider.StreamEvent) error {
	select {
	case ch <- ev:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// drain reads ch to exhaustion after CancelFunc fires, so the producer
// goroutine's close(ch) never blocks on a reader that stopped listening.
// Ranging over an already-closed channel returns immediately and never
// panics; only a send to a closed channel would.
func drain(ch <-chan provider.StreamEvent) {
	for range ch {
		continue
	}
}

// validateRequest refuses a malformed request before any collaborator is
// consulted.
func validateRequest(req provider.ModelRequest) error {
	if req.TaskID == "" || len(req.Inputs) == 0 {
		return ErrInvalidRequest
	}
	return nil
}

// toChatRequest, contains, mapProviderError, auditRefusal, auditOutcome
// and append moved to audit_helpers.go (P1-E11-W3-S23-T3) - named to
// avoid colliding with the "internal/conductor/dispatch.go" filename an
// unrelated, pre-existing testonly-allow.json entry (P1-E14-W3-S29-T2)
// already reserves as N/S-30's future dispatch-seam file - to keep this
// file
// under Art.10.3's 300-line cap once the streaming/one-shot terminal-event
// work landed here - a behavior-preserving split, same package, no
// signature changes (the same pattern envelope.go/output.go and
// daemon_unix.go/daemon_unix_run.go already use in this tree).

// ExecuteFanOut dispatches n legs of req through FanOut (fanout.go),
// R-21.214, using e.Execute as every leg's exec function. withPermit and
// journal are per-call parameters, not Executor fields: ExecutorConfig
// (pipeline.go) is outside this ticket's files_scope, see the journal.
func (e *Executor) ExecuteFanOut(ctx context.Context, req provider.ModelRequest, n int, completed map[int]JobID, withPermit WithPermitFn, journal JournalAppender) ([]provider.ModelResponse, error) {
	return FanOut(ctx, req, n, completed, withPermit, journal, e.Execute)
}
