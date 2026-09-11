package plugin

// Purpose: the guest-side invoke shim per R-14.50 (T0-181) — the wasm guest
//
//	exports a single `plugin_invoke` entry taking a JSON envelope; a
//	manifest's provides entry maps onto one of the five AgentProviderMethod
//	values (types.go); the host dispatches through the 7-function host-ABI.
//	GuestDispatcher is the binding a compiled guest package calls at its
//	plugin_invoke export site to route a decoded envelope to its own
//	AgentProvider-shaped handlers, without this package importing
//	pkg/provider (self-contained, like register.go's BuiltinHandlers).
//
// Inputs: raw JSON bytes crossing the host-ABI boundary.
// Outputs: raw JSON bytes (an InvokeResult), always — Dispatch never
//
//	panics and never returns a Go error; failure is carried inside the
//	returned envelope as InvokeError so a wasm export (which cannot return
//	a Go error) has a wire-stable way to report one.
//
// Constraints: pkg/plugin never imports internal/ (Art.10.2); must compile
//
//	under GOOS=wasip1 GOARCH=wasm (this ticket's AC) — stdlib
//	encoding/json only, no cgo, no os-specific branching; no bare
//	fmt.Errorf/errors.New for anything that crosses the taxonomy boundary
//	(boundary lint); R-14.50's actual wazero round-trip is O/S-33.T2's AC,
//	not this ticket's — this file provides only the envelope types and the
//	guest-side dispatch helper they compile against.
//
// SPORT: pkg/plugin guest-invoke-shim (ADD) — P1-E15-W4-S33-T1.

import (
	"context"
	"encoding/json"
)

// InvokeEnvelope is the JSON envelope crossing the guest's single
// plugin_invoke wasm export. Method names the AgentProviderMethod the
// manifest's provides entry bound the caller's request to; Params carries
// that method's request payload, left as raw JSON since its concrete shape
// (ChatRequest, ModelEmbedRequest, ...) belongs to pkg/provider, which this
// package does not import.
type InvokeEnvelope struct {
	// Method is the AgentProviderMethod being invoked, as its wire string
	// (see AgentProviderMethod.String).
	Method string `json:"method"`
	// Params is the method's request payload, opaque to this package.
	Params json.RawMessage `json:"params"`
}

// InvokeResult is what plugin_invoke returns for one InvokeEnvelope: either
// a Result payload or an Error, never neither and never both.
type InvokeResult struct {
	// Result is the method's response payload, opaque to this package.
	// Empty when Error is set.
	Result json.RawMessage `json:"result,omitempty"`
	// Error describes why the call failed. nil on success.
	Error *InvokeError `json:"error,omitempty"`
}

// InvokeError is the wire-stable failure shape InvokeResult carries when a
// plugin_invoke call cannot produce a Result. Code is a short machine
// vocabulary; Message is human-readable detail.
type InvokeError struct {
	// Code identifies the failure class: "malformed-envelope",
	// "unknown-method", or "handler-error".
	Code string `json:"code"`
	// Message is the human-readable detail.
	Message string `json:"message"`
}

// Error implements the error interface so an *InvokeError can be returned
// and inspected like any other Go error inside guest-side code, ahead of
// being marshaled back into the envelope Dispatch returns.
func (e *InvokeError) Error() string {
	return e.Code + ": " + e.Message
}

// GuestDispatchFunc is the guest-side handler signature a compiled guest
// registers for one AgentProviderMethod. A real guest binary's handler
// typically closes over its own AgentProvider implementation and forwards
// to the matching method (Chat, Embed, Count, Stream, or Capabilities)
// after unmarshaling params into that method's request type.
type GuestDispatchFunc func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)

// GuestDispatcher routes a decoded InvokeEnvelope to the GuestDispatchFunc
// registered for its Method. It holds no manifest and does no validation
// beyond "is this method registered" — the manifest's provides -> method
// binding is the host's and the plugin author's contract, documented on
// AgentProviderMethod, not enforced again here.
type GuestDispatcher struct {
	handlers map[AgentProviderMethod]GuestDispatchFunc
}

// NewGuestDispatcher returns an empty GuestDispatcher ready for Register
// calls.
func NewGuestDispatcher() *GuestDispatcher {
	return &GuestDispatcher{handlers: make(map[AgentProviderMethod]GuestDispatchFunc)}
}

// Register binds fn to method. A later Register call for the same method
// replaces the earlier binding — guest packages register once at init time
// in practice, so last-write-wins is a deliberate simplicity choice, not an
// oversight.
func (d *GuestDispatcher) Register(method AgentProviderMethod, fn GuestDispatchFunc) {
	d.handlers[method] = fn
}

// Dispatch decodes raw as an InvokeEnvelope, routes it to the registered
// handler for its Method, and returns the marshaled InvokeResult bytes.
// Dispatch never returns a Go error and never panics: a malformed envelope,
// an invalid or unregistered method, or a handler error are all carried
// inside the returned InvokeResult's Error field, because the wasm export
// this feeds has no Go-error return channel of its own.
func (d *GuestDispatcher) Dispatch(ctx context.Context, raw []byte) []byte {
	var env InvokeEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return encodeInvokeResult(InvokeResult{Error: &InvokeError{
			Code: "malformed-envelope", Message: err.Error(),
		}})
	}

	method := AgentProviderMethod(env.Method)
	handler, ok := d.handlers[method]
	if !method.Valid() || !ok {
		return encodeInvokeResult(InvokeResult{Error: &InvokeError{
			Code: "unknown-method", Message: "no handler registered for method " + env.Method,
		}})
	}

	result, err := handler(ctx, env.Params)
	if err != nil {
		return encodeInvokeResult(InvokeResult{Error: &InvokeError{
			Code: "handler-error", Message: err.Error(),
		}})
	}
	return encodeInvokeResult(InvokeResult{Result: result})
}

// encodeInvokeResult marshals res, falling back to a hand-built JSON error
// envelope in the (unreachable in practice, since InvokeResult always
// marshals cleanly) case json.Marshal itself fails — Dispatch's contract is
// to always return well-formed bytes.
func encodeInvokeResult(res InvokeResult) []byte {
	b, err := json.Marshal(res)
	if err != nil {
		return []byte(`{"error":{"code":"internal","message":"failed to encode invoke result"}}`)
	}
	return b
}
