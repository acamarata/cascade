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
	"sync/atomic"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Executor implements provider.ModelExecutor: the sole caller-facing model
// call door. Build one with NewExecutor; the zero value is not usable.
// cancelsOnce/cancelsRegistry/bridge (stream.go), spawnHook/spawnSem/
// spawnDrops (spawn_hook.go) and the usage seams (usage.go) are struct
// fields rather than ExecutorConfig parameters so every NewExecutor call
// site keeps compiling unchanged; each is lazily built or a nil no-op.
type Executor struct {
	pipeline        *Pipeline
	router          Router
	cancelsOnce     sync.Once
	cancelsRegistry *cancelRegistry
	bridge          EventBridge
	spawnHook       SpawnHook
	spawnSem        chan struct{}
	spawnDrops      int64
	usageStore      UsageRecorder
	usageAgg        UsageAggregator
	costEstimator   CostEstimator
	retry           atomic.Pointer[retrySeam] // transient_retry.go
}

// CancelFunc cancels an in-flight ExecuteStream job's context. It is
// idempotent and never panics on an already-closed stream channel.
type CancelFunc func()

var _ provider.ModelExecutor = (*Executor)(nil)

// Execute runs req to completion through the sole model.execute door. A
// call past authorization mints a JobID, registers its cancel func before
// dispatch and publishes exactly one terminal SSE event (R-21.217); a call
// refused before dispatch mints none.
func (e *Executor) Execute(ctx context.Context, req provider.ModelRequest) (provider.ModelResponse, error) {
	start := e.usageClockStart() // P1-E11-W3-S23-T4: see usage.go's CONTRACT NOTE.
	pass, err := e.enterDoor(ctx, doorCall{Verb: VerbExecute, Req: &req})
	if err != nil {
		return provider.ModelResponse{}, err
	}
	req, tier, sel := pass.Payload.(provider.ModelRequest), pass.Tier, pass.Sel

	rawID, idErr := cascade.NewID()
	if idErr != nil {
		return provider.ModelResponse{}, cascade.Wrap(cascade.KindInternal, idErr, "conductor: minting job id")
	}
	jobID := JobID(rawID)
	dctx, cancel := context.WithCancelCause(ctx)
	e.cancels().register(jobID, func() { cancel(errJobCancelled) })
	defer func() { cancel(nil); e.cancels().deregister(jobID) }()

	chatReq := toChatRequest(req)
	var resp provider.ChatResponse
	run := func(c context.Context, s provider.Selection) (provider.Usage, bool, error) {
		var derr error
		resp, derr = e.doorChat(c, s, chatReq)
		return resp.Usage, false, derr
	}
	finalSel, st, _, err := e.withRetry(dctx, req, sel, run, e.abandonedUsage(ctx, req, jobID, start))
	key := req.TaskID + "|" + req.TaskClass
	if err != nil {
		mapped := mapProviderError(err)
		outcome, cause := "provider_failure", mapped
		if dctx.Err() != nil {
			outcome, cause = "cancellation", context.Cause(dctx)
		}
		e.auditAttempts(ctx, VerbExecute, tier, key, finalSel, outcome, cause, st)
		e.publishTerminal(ctx, jobID, "error")
		e.attributeUsage(ctx, req, jobID, finalSel, st.Attempts, resp.Usage, err, start)
		return provider.ModelResponse{}, mapped
	}
	out := provider.ModelResponse{JobID: provider.JobID(jobID), Selection: finalSel, Output: resp.Message.Content, Usage: resp.Usage}
	e.auditAttempts(ctx, VerbExecute, tier, key, finalSel, "success", nil, st)
	e.publishTerminal(ctx, jobID, "done")
	e.attributeUsage(ctx, req, jobID, finalSel, st.Attempts, resp.Usage, nil, start)
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
	// The tier only narrows: the gate's answer and the thread's privacy mode
	// are joined into it, so neither can widen what the request declared.
	tier = provider.JoinSensitivity(tier, e.pipeline.cfg.Sensitivity.Resolve(tier))
	if tp, ok := ThreadPrivacyFrom(ctx); ok {
		tier = provider.JoinSensitivity(tier, tp.EffectiveMode())
	}
	if tier == provider.SensitivityLocalOnly && req.Policy.ExternalAllowed {
		return tier, ErrSensitivityViolation
	}
	// Policy judges the request at the tier the gate resolved, not the tier the
	// caller declared, so a gate that narrows public to restricted is enforced.
	resolved := req
	resolved.Sensitivity = tier
	if err := e.pipeline.cfg.Policy.Authorize(ctx, resolved); err != nil {
		return tier, cascade.Wrap(cascade.KindPolicyDenied, err, "conductor: policy refused")
	}
	return tier, nil
}

// ExecuteStream runs req as a streaming exchange: a buffered(1) channel of
// StreamEvent, closed when the provider finishes or CancelFunc is called.
func (e *Executor) ExecuteStream(ctx context.Context, req provider.ModelRequest) (<-chan provider.StreamEvent, CancelFunc, error) {
	rawID, err := cascade.NewID()
	if err != nil {
		return nil, nil, cascade.Wrap(cascade.KindInternal, err, "conductor: minting stream job id")
	}
	return e.executeStream(ctx, req, JobID(rawID))
}

// executeStream is ExecuteStream under a caller-minted job id, so usage
// attribution and the job id ExecuteStreamJob reports are the same value.
func (e *Executor) executeStream(ctx context.Context, req provider.ModelRequest, jobID JobID) (<-chan provider.StreamEvent, CancelFunc, error) {
	pass, err := e.enterDoor(ctx, doorCall{Verb: VerbExecuteStream, Req: &req})
	if err != nil {
		return nil, nil, err
	}
	ch := make(chan provider.StreamEvent, 1)
	cctx, cancel := context.WithCancelCause(ctx)
	var once sync.Once
	cancelFn := CancelFunc(func() {
		once.Do(func() {
			cancel(errJobCancelled)
			go drain(ch)
		})
	})
	go e.runStream(cctx, pass.Payload.(provider.ModelRequest), jobID, pass, ch)
	return ch, cancelFn, nil
}

// runStream drives one streaming dispatch to completion under the retry
// policy (transient_retry.go): a transient failure or a capability denial
// before any output reached the caller is retried or fails over once, every
// attempt is attributed, and exactly one terminal audit record is written
// (success, provider failure, or cancellation carrying context.Cause).
func (e *Executor) runStream(ctx context.Context, req provider.ModelRequest, jobID JobID, pass doorPass, ch chan<- provider.StreamEvent) {
	defer close(ch)
	start := e.usageClockStart()
	chatReq := toChatRequest(req)
	var last *streamAttempt
	run := func(c context.Context, s provider.Selection) (provider.Usage, bool, error) {
		var ignored atomic.Bool // streamAttempt tracks what reached the caller
		last = &streamAttempt{ctx: c, ch: ch}
		return last.finish(e.doorStream(c, s, chatReq, &ignored, last.sink))
	}
	dctx := context.WithoutCancel(ctx)
	sel, st, u, err := e.withRetry(ctx, req, pass.Sel, run, e.abandonedUsage(dctx, req, jobID, start))
	outcome, cause := "success", error(nil)
	if err != nil {
		outcome, cause = "provider_failure", err
		if ctx.Err() != nil {
			outcome, cause = "cancellation", context.Cause(ctx)
		}
	}
	e.auditAttempts(dctx, VerbExecuteStream, pass.Tier, req.TaskID+"|"+req.TaskClass, sel, outcome, cause, st)
	e.attributeUsage(dctx, req, jobID, sel, st.Attempts, u, err, start)
	if err != nil && outcome != "cancellation" && (last == nil || !last.errSent) {
		_ = send(ctx, ch, provider.StreamEvent{Kind: provider.StreamEventError, Err: err})
	}
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
// already reserves as N/S-30's future dispatch-seam file - to keep this file
// under Art.10.3's 300-line cap once the streaming/one-shot terminal-event
// work landed here - a behavior-preserving split, same package, no
// signature changes (as envelope.go/output.go and daemon_unix*.go do).

// ExecuteFanOut dispatches n legs of req through FanOut (fanout.go),
// R-21.214: e.Execute is every leg's exec function and e.Authorize the
// replay door. withPermit, journal and results are explicit per-call
// parameters; nil is ErrConstructionFailed, never a silent skip.
func (e *Executor) ExecuteFanOut(ctx context.Context, fanoutID string, req provider.ModelRequest, n int, completed map[int]JobID, withPermit WithPermitFn, journal JournalAppender, results LegResultStore) ([]provider.ModelResponse, error) {
	return FanOut(ctx, fanoutID, req, n, completed, withPermit, journal, results, e.Authorize, e.Execute)
}

// Authorize is the door check Execute runs (readiness, then authorize:
// validation, classifier, sensitivity, policy) as an AuthorizeFn, with a
// refusal audited through the executor's audit writer. No provider call.
func (e *Executor) Authorize(ctx context.Context, req provider.ModelRequest) error {
	if err := e.pipeline.Ready(); err != nil {
		return err
	}
	tier, err := e.authorize(ctx, req)
	if err != nil {
		e.auditRefusal(ctx, req, tier, err)
	}
	return err
}
