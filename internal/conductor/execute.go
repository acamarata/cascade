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
	"encoding/json"
	"sync"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Executor implements provider.ModelExecutor: the sole caller-facing model
// call door. Build one with NewExecutor; the zero value is not usable.
type Executor struct {
	pipeline *Pipeline
	router   Router
}

// CancelFunc cancels an in-flight ExecuteStream job. It is idempotent
// (safe to call more than once) and never panics on an already-closed
// stream channel. It cancels the job's context only; the wire-level
// job.cancel JSON-RPC bridge over POST /rpc is S-23.T3's (R-40.X9).
type CancelFunc func()

var _ provider.ModelExecutor = (*Executor)(nil)

// Execute runs req to completion through the sole model.execute door.
func (e *Executor) Execute(ctx context.Context, req provider.ModelRequest) (provider.ModelResponse, error) {
	if err := e.pipeline.Ready(); err != nil {
		return provider.ModelResponse{}, err
	}
	tier, err := e.authorize(ctx, req)
	if err != nil {
		e.auditRefusal(ctx, req, tier, err)
		return provider.ModelResponse{}, err
	}
	sel, err := e.router.Select(ctx, req)
	if err != nil {
		e.auditRefusal(ctx, req, tier, ErrNoLane)
		return provider.ModelResponse{}, ErrNoLane
	}
	req, err = e.substituteInputs(ctx, req, tier)
	if err != nil {
		e.auditOutcome(ctx, req, tier, sel, "egress_substitution_failed", err)
		return provider.ModelResponse{}, ErrEgressSubstitutionFailed
	}
	resp, finalSel, err := e.dispatchWithFailover(ctx, sel, req)
	if err != nil {
		mapped := mapProviderError(err)
		e.auditOutcome(ctx, req, tier, finalSel, "provider_failure", mapped)
		return provider.ModelResponse{}, mapped
	}
	jobID, err := cascade.NewID()
	if err != nil {
		return provider.ModelResponse{}, cascade.Wrap(cascade.KindInternal, err, "conductor: minting job id")
	}
	out := provider.ModelResponse{
		JobID:     provider.JobID(jobID),
		Selection: finalSel,
		Output:    resp.Message.Content,
		Usage:     resp.Usage,
	}
	e.auditOutcome(ctx, req, tier, finalSel, "success", nil)
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

// toChatRequest projects a ModelRequest onto the ModelProvider driver
// boundary's ChatRequest shape.
func toChatRequest(req provider.ModelRequest) provider.ChatRequest {
	return provider.ChatRequest{
		Messages: req.Inputs,
		RequiredCapabilities: provider.RequiredCapabilities{
			Search:           contains(req.RequiredCapabilities, "search"),
			URLFetch:         contains(req.RequiredCapabilities, "url_fetch"),
			Vision:           contains(req.RequiredCapabilities, "vision"),
			ToolUse:          contains(req.RequiredCapabilities, "tool_use"),
			LongContext:      contains(req.RequiredCapabilities, "long_context"),
			StructuredOutput: contains(req.RequiredCapabilities, "structured_output"),
		},
	}
}

func contains(caps provider.RequiredCapabilities, name string) bool {
	switch name {
	case "search":
		return caps.Search
	case "url_fetch":
		return caps.URLFetch
	case "vision":
		return caps.Vision
	case "tool_use":
		return caps.ToolUse
	case "long_context":
		return caps.LongContext
	case "structured_output":
		return caps.StructuredOutput
	}
	return false
}

// mapProviderError maps a provider-side error onto the A-T7 taxonomy. An
// error already carrying a taxonomy Kind passes through unchanged; anything
// else is wrapped KindInternal rather than surfaced raw.
func mapProviderError(err error) error {
	if _, ok := cascade.KindOf(err); ok {
		return err
	}
	return cascade.Wrap(cascade.KindInternal, err, "conductor: provider dispatch failed")
}

// auditRefusal writes the pre-dispatch-rejection audit record.
func (e *Executor) auditRefusal(ctx context.Context, req provider.ModelRequest, tier provider.SensitivityTier, cause error) {
	e.append(ctx, req, tier, provider.Selection{}, "refusal", cause)
}

// auditOutcome writes the terminal audit record for a dispatched request.
func (e *Executor) auditOutcome(ctx context.Context, req provider.ModelRequest, tier provider.SensitivityTier, sel provider.Selection, outcome string, cause error) {
	e.append(ctx, req, tier, sel, outcome, cause)
}

// append is the single audit call site every terminal outcome routes
// through, so Execute and ExecuteStream never write more or fewer than one
// record per call. The Explain payload carries only the redacted trace and
// cost accounting - never a raw request field.
func (e *Executor) append(ctx context.Context, req provider.ModelRequest, tier provider.SensitivityTier, sel provider.Selection, outcome string, cause error) {
	trace := ExecutionTrace{LaneID: sel.LaneID, Outcome: outcome}
	if cause != nil {
		trace.Reason = cause.Error()
	}
	explain, _ := json.Marshal(trace)
	paramsHash := audit.HashParams([]byte(req.TaskID + "|" + req.TaskClass))
	_, _ = e.pipeline.cfg.Audit.Append(ctx, audit.Event{
		Kind:       audit.KindPolicyRoute,
		Actor:      "conductor",
		Action:     "model.execute",
		ParamsHash: paramsHash,
		RiskLevel:  tier.String(),
		Verdict:    outcome,
		Outcome:    outcome,
		Explain:    explain,
	})
}
