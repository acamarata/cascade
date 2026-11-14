package coretools

// Purpose: the delegation tool set -- cascade_conductor_delegate hands an
//   intent to the conductor and cascade_conductor_delegate_wait reads the
//   outcome of a job already handed over. Split into its own sibling file
//   because specs.go sits near the 300-line cap.
// Inputs: none -- a declaration, matching specs.go's own contract.
// Outputs: two Specs, served by the daemon's conductor.delegate and
//   conductor.delegate_wait methods.
// Constraints: both schemas refuse extra properties. A delegate answer is
//   {job_id, status} or a typed error {kind, message}; a raw Go error
//   string never reaches the harness, so a handler failure is re-encoded
//   by typedDelegateErrors.

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// CapabilityAgentsDelegate covers handing work to the conductor and
// reading its outcome.
const CapabilityAgentsDelegate = "agents.delegate"

// delegateSpecs are the tools over conductor.delegate and
// conductor.delegate_wait. Neither descends from a v1 tool.
func delegateSpecs() []Spec {
	return []Spec{
		// delegateParams (cmd/cascade/delegate_rpc.go).
		{
			Name:   "cascade_conductor_delegate",
			V1Name: "NEW-IN-V2:cascade_conductor_delegate",
			Description: "Delegate an intent to the conductor, which routes it to an agent. " +
				"Returns the job id and its status. Refused when the daemon is not running.",
			Method:     "conductor.delegate",
			Capability: CapabilityAgentsDelegate,
			Mutating:   true,
			Schema: object([]string{"intent"}, map[string]any{
				"intent":          str("What the delegated agent should do."),
				"task_class":      str("Task class used to pick a provider."),
				"min_quality":     str("Minimum quality tier the answer must meet."),
				"deadline":        str("Deadline for the delegation."),
				"mutation":        boolean("The agent may change files in the job's worktree."),
				"job_id":          str("Attach the delegation to this existing job."),
				"wait_timeout_ms": num("Milliseconds to wait for a terminal state before returning."),
			}),
		},
		// delegateWaitParams (cmd/cascade/delegate_rpc.go).
		{
			Name:   "cascade_conductor_delegate_wait",
			V1Name: "NEW-IN-V2:cascade_conductor_delegate_wait",
			Description: "Wait for a delegated job to reach a terminal state. " +
				"Returns the job id and its status. Read-only.",
			Method:     "conductor.delegate_wait",
			Capability: CapabilityAgentsDelegate,
			Schema: object([]string{"job_id"}, map[string]any{
				"job_id":     str("The job id returned by cascade_conductor_delegate."),
				"timeout_ms": num("Milliseconds to wait before returning the current status."),
			}),
		},
	}
}

// typedToolError is a tool failure whose text is the JSON object
// {"kind", "message"}; internal/mcp sends Error() as the refusal message.
type typedToolError struct {
	body  string
	cause error
}

func (e *typedToolError) Error() string { return e.body }

// Unwrap keeps the taxonomy kind reachable for callers that classify errors.
func (e *typedToolError) Unwrap() error { return e.cause }

// typedDelegateErrors re-encodes every failure of a delegate tool handler
// as a typedToolError. A taxonomy error keeps its kind and message; an
// internal error, however it arrived, has its text withheld.
func typedDelegateErrors(spec Spec, h func(context.Context, []byte) ([]byte, error)) func(context.Context, []byte) ([]byte, error) {
	if spec.Capability != CapabilityAgentsDelegate {
		return h
	}
	return func(ctx context.Context, in []byte) ([]byte, error) {
		out, err := h(ctx, in)
		if err == nil {
			return out, nil
		}
		kind, msg := cascade.KindInternal, spec.Method+": the delegation failed"
		var ce *cascade.Error
		if errors.As(err, &ce) {
			kind, msg = ce.Kind, ce.Msg
		}
		if kind == cascade.KindInternal {
			msg = spec.Method + ": the delegation failed"
		}
		var cause error
		if ce != nil {
			cause = ce
		}
		body, mErr := json.Marshal(map[string]string{"kind": kind.String(), "message": msg})
		if mErr != nil {
			return nil, cascade.Wrap(cascade.KindInternal, mErr, "mcp: encoding a typed tool error")
		}
		return nil, &typedToolError{body: string(body), cause: cause}
	}
}
