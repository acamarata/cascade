package nodes

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the NODE-side leg of a dispatch — admit the action
//
//	durably, then answer with a signed result frame.
//
// Inputs: the attempt the node claimed, and the action it carries.
// Outputs: a signed DispatchFrame the controller verifies against this
//
//	node's device record.
//
// Constraints: the ORDER is the guarantee. The action is reserved — written
//
//	and fsynced — BEFORE anything runs, so a crash between executing and
//	recording cannot let a redelivery run it a second time. A duplicate is
//	answered with OutcomeRefused rather than an error: the work already
//	happened, which is success from the controller's point of view, and
//	reporting it as a failure would invite exactly the retry that must not
//	occur.
//
//	Signing goes through a FrameSigner (NodeKeystore.Sign in production), so
//	the node's private key never leaves custody to produce a result frame.
//
//	`cascade node serve` refuses to run on Windows per the S-36.T2 tier-2
//	ruling, so this leg is unreachable there by construction rather than by
//	a platform branch here.
//
// SPORT: internal/nodes:dispatch-execute (ADD) — P1-E17-W4-S37-T2.

// ExecuteMethod is the verb the node answers a claimed dispatch on.
const ExecuteMethod = "node.dispatch.execute"

// ExecuteRequest is one claimed attempt handed to the node.
type ExecuteRequest struct {
	DispatchID string `json:"dispatch_id"`
	Attempt    uint64 `json:"attempt"`
	ActionID   string `json:"action_id"`
	Idempotent bool   `json:"idempotent"`
	// ResultCommit is where this node pushed its results, empty when the
	// action produced none.
	ResultCommit string `json:"result_commit,omitempty"`
	// Outcome is what running the action produced. An unrecognized value
	// is refused rather than forwarded.
	Outcome DispatchOutcome `json:"outcome"`
}

// ExecuteDeps are the node leg's collaborators.
type ExecuteDeps struct {
	// Actions is the node's durable action log.
	Actions ActionLog
	// Sign produces the frame's signature.
	Sign FrameSigner
	// Self identifies this node.
	Self Identity
	// EnrollmentID binds the frame to this node's enrollment.
	EnrollmentID string
	// Sequences mints this node's monotonic frame sequence.
	Sequences *SequenceStore
}

// ExecuteDispatch admits the claimed action and returns a signed frame.
func ExecuteDispatch(ctx context.Context, deps ExecuteDeps, req ExecuteRequest) (DispatchFrame, error) {
	if err := requireExecuteFields(req); err != nil {
		return DispatchFrame{}, err
	}
	if deps.Sequences == nil {
		return DispatchFrame{}, cascade.New(cascade.KindInternal,
			"nodes: the node dispatch leg has no sequence store")
	}

	outcome := req.Outcome
	action := Action{ID: req.ActionID, Idempotent: req.Idempotent}
	if err := ReserveAction(ctx, deps.Actions, action); err != nil {
		// A duplicate is not a failure: the work already happened. It is
		// reported as a refusal so the controller records a conflict
		// rather than retrying.
		if !isDuplicateAction(err) {
			return DispatchFrame{}, err
		}
		outcome = OutcomeRefused
	}

	// The node mints its own monotonic sequence: the controller refuses a
	// frame at or below the last one it accepted, so a replayed frame is
	// rejected without the controller having to remember the frames
	// themselves.
	seq := deps.Sequences.Last(deps.Self.NodeID) + 1
	frame := DispatchFrame{
		NodeID:       deps.Self.NodeID,
		EnrollmentID: deps.EnrollmentID,
		Sequence:     seq,
		DispatchID:   req.DispatchID,
		Attempt:      req.Attempt,
		ActionID:     req.ActionID,
		Outcome:      outcome,
		ResultCommit: req.ResultCommit,
	}
	signed, err := SignDispatchFrame(ctx, frame, deps.Sign)
	if err != nil {
		return DispatchFrame{}, err
	}
	deps.Sequences.Advance(deps.Self.NodeID, seq)
	if outcome != OutcomeRefused {
		if err := deps.Actions.Complete(ctx, req.ActionID, outcome); err != nil {
			return DispatchFrame{}, err
		}
	}
	return signed, nil
}

// requireExecuteFields refuses a claim missing what the frame needs.
func requireExecuteFields(req ExecuteRequest) error {
	for field, value := range map[string]string{
		"dispatch_id": req.DispatchID,
		"action_id":   req.ActionID,
	} {
		if value == "" {
			return cascade.Newf(cascade.KindInvalidInput, "nodes: %s requires %s", ExecuteMethod, field)
		}
	}
	if req.Attempt == 0 {
		return cascade.Newf(cascade.KindInvalidInput,
			"nodes: %s requires the attempt it was claimed under", ExecuteMethod)
	}
	if !validOutcome(req.Outcome) {
		return cascade.Newf(cascade.KindInvalidInput,
			"nodes: %s: outcome %q is not one this build recognizes", ExecuteMethod, req.Outcome)
	}
	return nil
}

// isDuplicateAction reports whether err is the duplicate-action refusal.
func isDuplicateAction(err error) bool {
	kind, ok := cascade.KindOf(err)
	return ok && kind == cascade.KindConflict
}

// RegisterExecuteHandler mounts ExecuteMethod on the node's serve registry.
func RegisterExecuteHandler(registry *rpc.Registry, deps ExecuteDeps) {
	registry.Register(ExecuteMethod, func(ctx context.Context, params json.RawMessage) (any, error) {
		var req ExecuteRequest
		if err := decodeParams(params, &req, ExecuteMethod); err != nil {
			return nil, err
		}
		return ExecuteDispatch(ctx, deps, req)
	})
}
