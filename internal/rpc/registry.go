package rpc

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

// HandlerFunc handles one dispatched JSON-RPC method call. It receives the
// request's raw params bytes (possibly nil/empty) and returns either a
// result value (marshaled into the response envelope's "result") or an
// error. A returned error that carries a pkg/cascade Kind (via
// cascade.KindOf) is wire-mapped through cascade.NewRPCError; any other
// error falls back to RPCCodeInternal, matching pkg/cascade's own documented
// fallback behavior.
type HandlerFunc func(ctx context.Context, params json.RawMessage) (any, error)

// MiddlewareFunc wraps a HandlerFunc to produce another HandlerFunc.
// elevation.go's Middleware is the one middleware this ticket registers by
// default; Registry.Use accepts any number of others.
type MiddlewareFunc func(method string, next HandlerFunc) HandlerFunc

// Registry resolves method names to handlers and runs the registered
// middleware chain around every dispatched call. Safe for concurrent
// Register/Dispatch (RWMutex-guarded) — the daemon registers all methods
// once at startup, before serving, but concurrent Dispatch calls from
// multiple in-flight requests are the normal case.
type Registry struct {
	mu         sync.RWMutex
	handlers   map[string]HandlerFunc
	middleware []MiddlewareFunc
}

// NewRegistry returns an empty Registry ready for Register/Use calls.
func NewRegistry() *Registry {
	return &Registry{handlers: make(map[string]HandlerFunc)}
}

// Register binds method to handler. Registering the same method twice
// overwrites the prior binding — the daemon composition root is the only
// caller and is expected to register each method exactly once; a
// duplicate registration is not itself an error (Article-1 does not
// require a redundant assertion here, and a hot-reloadable plugin-provided
// method may legitimately re-register).
func (r *Registry) Register(method string, handler HandlerFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[method] = handler
}

// Use appends mw to the middleware chain. Middleware runs in registration
// order around every dispatched call — the first registered middleware is
// the outermost wrapper, matching the ticket contract's "applied in
// registration order".
func (r *Registry) Use(mw MiddlewareFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.middleware = append(r.middleware, mw)
}

// Registered reports whether method has a bound handler, without invoking
// it. handler.go's SkewCheck ordering does not need this, but tests and
// future status/introspection callers do.
func (r *Registry) Registered(method string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.handlers[method]
	return ok
}

// Dispatch resolves req.Method, chains the registered middleware around it
// in registration order, and invokes the result. An unregistered method
// returns codeMethodNotFound (-32601) directly, before any middleware
// runs — an unknown method never reaches the elevation middleware, since
// there is nothing to elevate into. A handler error is converted to the
// wire ErrorObject shape via errorObjectFrom.
func (r *Registry) Dispatch(ctx context.Context, req *Request) (any, *ErrorObject) {
	r.mu.RLock()
	handler, ok := r.handlers[req.Method]
	chain := make([]MiddlewareFunc, len(r.middleware))
	copy(chain, r.middleware)
	r.mu.RUnlock()

	if !ok {
		return nil, methodNotFoundError(req.Method)
	}

	wrapped := handler
	for i := len(chain) - 1; i >= 0; i-- {
		wrapped = chain[i](req.Method, wrapped)
	}

	result, err := wrapped(ctx, req.Params)
	if err != nil {
		return nil, errorObjectFrom(err)
	}
	return result, nil
}

// methodNotFoundError builds the -32601 error for an unregistered method.
// See jsonrpc.go's codeMethodNotFound doc comment for how this literal
// spec-reserved code reconciles with pkg/cascade's KindNotFound.
func methodNotFoundError(method string) *ErrorObject {
	return &ErrorObject{
		Code:    codeMethodNotFound,
		Message: "method not found: " + method,
		Data:    map[string]string{"kind": cascade.KindNotFound.String()},
	}
}

// errorObjectFrom converts a HandlerFunc error into the wire ErrorObject
// shape. An *ErrorObject error (e.g. one elevation.go already built) passes
// through unchanged; anything else routes through pkg/cascade.NewRPCError so
// taxonomy Kinds get their existing RPCCode* mapping and non-taxonomy
// errors fall back to RPCCodeInternal — the exact behavior NewRPCError
// documents.
func errorObjectFrom(err error) *ErrorObject {
	if eo, ok := err.(*ErrorObject); ok {
		return eo
	}
	rpcErr := cascade.NewRPCError(err)
	return &ErrorObject{Code: rpcErr.Code, Message: rpcErr.Message, Data: rpcErr.Data}
}
