package coretools

// Purpose: turn Specs() into mcp.CoreRegistration values whose handlers
//   dispatch through the process's OWN JSON-RPC method table — the same
//   handler value the daemon serves, never a second implementation of it
//   (P1-E16-W4-S34-T2).
// Inputs: a Dispatcher (the process's *rpc.Registry in production).
// Outputs: one registration per spec whose method that process actually
//   serves.
// Constraints: Art.1. A spec whose method is not registered in this
//   process is NOT exposed, because a tool that answers "method not
//   found" is a stub wearing a schema. The capability filter is applied
//   by internal/mcp at registry construction, not here: this package
//   declares what a tool needs, the registry decides who may see it.
// SPORT: internal/mcp/coretools (ADD) — P1-E16-W4-S34-T2.

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Dispatcher is the process's RPC method table, narrowed to the two
// operations this package needs. Its methods are transcribed from
// *rpc.Registry's own, and the assertion below pins them: a change to
// either signature breaks this build rather than drifting past it.
type Dispatcher interface {
	Registered(method string) bool
	Dispatch(ctx context.Context, req *rpc.Request) (any, *rpc.ErrorObject)
}

var _ Dispatcher = (*rpc.Registry)(nil)

// Registrations returns one mcp.CoreRegistration per spec whose method
// dispatcher actually serves.
//
// A nil dispatcher returns nothing at all. That is the honest answer for
// a process with no method table: every tool here is a view onto an RPC
// method, so with no methods there are no tools — not tools that fail
// when called.
func Registrations(dispatcher Dispatcher) []mcp.CoreRegistration {
	if dispatcher == nil {
		return nil
	}
	var out []mcp.CoreRegistration
	for _, spec := range Specs() {
		if !exposable(spec) || !dispatcher.Registered(spec.Method) {
			continue
		}
		out = append(out, registrationFor(dispatcher, spec, spec.Name))
		if spec.Alias != "" {
			// The alias is bound to a registration built from the SAME
			// spec, so the two names cannot answer differently — the rule
			// recall.Handler.Register already applies to its own
			// cascade_search alias, restated at this layer.
			out = append(out, registrationFor(dispatcher, spec, spec.Alias))
		}
	}
	return out
}

// exposable reports whether spec may appear on the MCP surface at all,
// independently of whether this process happens to serve its method.
//
// AN ELEVATED VERB IS NEVER AN MCP TOOL (07's header rule). Decided here,
// at the moment of registration, rather than only asserted in a test: MCP
// carries no attestation, so a verb the elevation table lists — even one
// it lists only CONDITIONALLY — would arrive at its handler through this
// surface with no gate in front of it at all.
//
// A refused spec is skipped silently, like a tool the capability filter
// hides, because a client that could tell "denied" from "does not exist"
// would learn that a privileged tool exists.
//
// Its own function so it can be exercised against a verb that is actually
// elevated: nothing in Specs() is today, and a branch no test can reach
// is a branch that stops working without anybody noticing.
func exposable(spec Spec) bool {
	return !rpc.IsElevatedVerb(spec.Method)
}

// Unservable names every spec whose method this dispatcher does not
// serve. It is the other half of Registrations, for a composition root
// that wants to log what it could not expose rather than wonder.
func Unservable(dispatcher Dispatcher) []string {
	var out []string
	for _, spec := range Specs() {
		if dispatcher == nil || !dispatcher.Registered(spec.Method) {
			out = append(out, spec.Name)
		}
	}
	return out
}

// registrationFor builds one registration for spec under the given name.
func registrationFor(dispatcher Dispatcher, spec Spec, name string) mcp.CoreRegistration {
	return mcp.CoreRegistration{
		Tool: mcp.Tool{
			Name:        name,
			Description: spec.Description,
			PluginID:    PluginID,
			InputSchema: spec.Schema,
		},
		// "read" is the grant vocabulary internal/mcp's pre-existing
		// isExposable filter understands. It is not this tool's
		// authorization — RequiredCapability is, and the policy engine
		// answers for it. Both run; neither replaces the other.
		Grants:             []string{"read"},
		RequiredCapability: spec.Capability,
		Handler:            handlerFor(dispatcher, spec.Method),
	}
}

// handlerFor adapts one RPC method to the MCP handler shape.
//
// The empty-input case matters: an MCP client may call a tool with no
// arguments at all, and a handler that passed `[]byte(nil)` to a method
// expecting an object would turn "you sent nothing" into a decode error
// naming the wrong problem. An absent argument object is sent on as `{}`,
// which each method's own params decoder then judges against its own
// required fields.
func handlerFor(dispatcher Dispatcher, method string) func(context.Context, []byte) ([]byte, error) {
	return func(ctx context.Context, input []byte) ([]byte, error) {
		params := json.RawMessage(input)
		if len(params) == 0 {
			params = json.RawMessage("{}")
		}
		result, rpcErr := dispatcher.Dispatch(ctx, &rpc.Request{
			JSONRPC: jsonrpcVersion, Method: method, Params: params,
		})
		if rpcErr != nil {
			return nil, errorFrom(method, rpcErr)
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return nil, cascade.Wrapf(cascade.KindInternal, err, "mcp: encoding %s result", method)
		}
		return encoded, nil
	}
}

// jsonrpcVersion is the only version this transport speaks, written the
// same way every other in-tree request builder writes it.
const jsonrpcVersion = "2.0"

// errorFrom turns a JSON-RPC error object back into a taxonomy error.
//
// The code is the canonical carrier: rpc's errorObjectFrom wire-maps the
// method's own Kind onto it, and cascade.KindFromJSONRPCCode maps it back,
// so the refusal a tool caller sees carries the same classification the
// RPC caller would have seen. A code this build cannot map — which the
// wire format permits, since the spec reserves codes we do not mint — is
// KindInternal rather than a guess: this layer has no basis to classify
// someone else's failure.
func errorFrom(method string, rpcErr *rpc.ErrorObject) error {
	kind := cascade.KindInternal
	if mapped, ok := cascade.KindFromJSONRPCCode(rpcErr.Code); ok {
		kind = mapped
	}
	return cascade.Newf(kind, "mcp: %s: %s", method, rpcErr.Message)
}
