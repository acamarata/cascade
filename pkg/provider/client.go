// Purpose: the typed model.execute door for plugins/pbd and internal/fleet
//   (R-21.280, R-40.X7): the frozen D/S-07.T3 client (internal/client.Client)
//   gains no new method, and every caller reaches the daemon's real
//   "conductor.execute" door and "job.cancel" through this file's Client
//   wrapper instead.
// Inputs: an RPCCaller - the one-method seam the frozen client already
//   satisfies - and, per call, a ModelRequest or a JobID.
// Outputs: a ModelResponse, or a taxonomy error; JobCancel returns only a
//   taxonomy error, or nil.
// Constraints: Client holds no socket, no transport, and no state beyond
//   its RPCCaller, so pkg/ stays free of internal/ imports (Art.10.2).
//   FIXED (T0-OPEN-FOLLOWUPS.md, R-16.80 addendum): ModelExecute used to
//   dial "model.execute", a method nothing in the tree ever registered,
//   marshaling ModelRequest directly - whose Sensitivity field has no
//   MarshalJSON, so even renaming the method alone would still fail to
//   decode against the real door. The real, registered door is
//   internal/daemon/conductor_execute.go's "conductor.execute", which
//   decodes sensitivity as a STRING name
//   (internal/daemon/conductor_execute_params.go's conductorExecuteParams).
//   ModelExecute now dials that method through modelExecuteWireParams, an
//   unexported struct in this package mirroring conductorExecuteParams
//   field-for-field (task_id, task_class, inputs[{role,content}],
//   requirements, sensitivity as SensitivityTier.String(), fan_out
//   omitempty) - one door, not two: no second daemon handler was added,
//   and the exported ModelExecute(ctx, ModelRequest) (ModelResponse,
//   error) signature is unchanged. ModelRequest's Policy,
//   RequiredCapabilities and ReservationID fields have no wire
//   counterpart in conductorExecuteParams and are not sent - the real
//   door does not decode them yet; this mirrors, rather than widens, its
//   actual contract. job.cancel is an idempotent JSON-RPC method
//   (R-21.264) - JobCancel itself holds no dedupe state and simply
//   forwards every call; a repeat call is a no-op only because the
//   daemon's own handler is idempotent, not because this wrapper
//   remembers anything. Cancellation is never written to the SSE stream
//   (GET /events is delivery only).
// SPORT: pkg.provider.client-wrapper/ADD (P1-E10-W3-S19-T1); FIX
//   (T0-OPEN-FOLLOWUPS.md, R-16.80 addendum).

package provider

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// modelExecuteMethod is the JSON-RPC method name ModelExecute dials: the
// daemon's real, registered "conductor.execute" door
// (internal/daemon/conductor_execute.go's ConductorExecuteMethod - not
// imported here, since pkg/provider never imports internal/, per this
// file's Art.10.2 constraint; asserted directly by client_test.go's
// TestClientModelExecuteWrapper).
const modelExecuteMethod = "conductor.execute"

// jobCancelMethod is the JSON-RPC method name JobCancel dials.
const jobCancelMethod = "job.cancel"

// modelExecuteWireMessage mirrors internal/daemon/
// conductor_execute_params.go's conductorExecuteMessage field-for-field:
// one ChatMessage turn on the wire.
type modelExecuteWireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// modelExecuteWireParams is "conductor.execute"'s request params shape,
// mirroring internal/daemon/conductor_execute_params.go's
// conductorExecuteParams field-for-field. It is duplicated here rather
// than imported - pkg/provider never imports internal/ (Art.10.2), the
// same reasoning conductorExecuteParams's own header gives for
// duplicating cmd/cascade/run.go's runRequestParams. Sensitivity is sent
// as the tier's String() name, never ModelRequest.Sensitivity's raw
// SensitivityTier numeric encoding, since SensitivityTier has no
// MarshalJSON and the real door decodes sensitivity as a string.
type modelExecuteWireParams struct {
	TaskID       string                    `json:"task_id"`
	TaskClass    string                    `json:"task_class"`
	Inputs       []modelExecuteWireMessage `json:"inputs"`
	Requirements Requirements              `json:"requirements"`
	Sensitivity  string                    `json:"sensitivity"`
	FanOut       int                       `json:"fan_out,omitempty"`
}

// toWireParams translates req into "conductor.execute"'s wire shape.
func (req ModelRequest) toWireParams() modelExecuteWireParams {
	inputs := make([]modelExecuteWireMessage, len(req.Inputs))
	for i, m := range req.Inputs {
		inputs[i] = modelExecuteWireMessage(m)
	}
	return modelExecuteWireParams{
		TaskID:       req.TaskID,
		TaskClass:    req.TaskClass,
		Inputs:       inputs,
		Requirements: req.Requirements,
		Sensitivity:  req.Sensitivity.String(),
		FanOut:       req.FanOut,
	}
}

// RPCCaller is the minimal one-method interface the frozen D/S-07.T3 client
// (internal/client.Client) already satisfies (R-21.281): Do issues one
// JSON-RPC 2.0 call, decoding its result into out. Client is built on this
// interface, not on the concrete client type, so pkg/provider never imports
// internal/client.
type RPCCaller interface {
	Do(ctx context.Context, method string, params, out any) error
}

// Client is a thin value wrapping an RPCCaller with the typed model.execute
// door's method wrappers. It holds no socket, no transport, and no state of
// its own beyond that RPCCaller.
type Client struct {
	caller RPCCaller
}

// NewClient builds a Client dialing every call through c.
func NewClient(c RPCCaller) *Client {
	return &Client{caller: c}
}

// jobCancelParams is job.cancel's request params shape.
type jobCancelParams struct {
	JobID JobID `json:"job_id"`
}

// ModelExecute dispatches req to the daemon's real "conductor.execute"
// door and returns its ModelResponse. It is exactly one
// Do(ctx, "conductor.execute", wire, &resp) call, where wire is req
// translated to the door's own wire params shape (toWireParams); any
// error Do returns is mapped through the pkg/cascade taxonomy before it
// reaches the caller.
func (c *Client) ModelExecute(ctx context.Context, req ModelRequest) (ModelResponse, error) {
	var resp ModelResponse
	if err := c.caller.Do(ctx, modelExecuteMethod, req.toWireParams(), &resp); err != nil {
		return ModelResponse{}, taxonomyError(err, "provider: conductor.execute failed")
	}
	return resp, nil
}

// JobCancel requests cancellation of jobID via the daemon's idempotent
// job.cancel door. A repeat call for the same jobID is a no-op returning
// the same result, because job.cancel itself is idempotent server-side;
// JobCancel holds no state of its own to enforce that - it forwards every
// call through caller.Do unconditionally.
func (c *Client) JobCancel(ctx context.Context, jobID JobID) error {
	if err := c.caller.Do(ctx, jobCancelMethod, jobCancelParams{JobID: jobID}, nil); err != nil {
		return taxonomyError(err, "provider: job.cancel failed")
	}
	return nil
}

// taxonomyError ensures err carries a pkg/cascade taxonomy Kind before it
// crosses this boundary: an err that already carries one (as the real
// internal/client.Client's Do always produces) passes through unchanged; an
// RPCCaller fake that returns a plain error is wrapped as KindInternal, the
// taxonomy's fallback kind for an unclassified failure.
func taxonomyError(err error, msg string) error {
	if _, ok := cascade.KindOf(err); ok {
		return err
	}
	return cascade.Wrap(cascade.KindInternal, err, msg)
}
