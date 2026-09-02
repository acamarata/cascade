package cascade

// This file holds the taxonomy's wire representations: the JSON-RPC 2.0
// error envelope (also reused verbatim for plugin RPC, per A-T7's HOW
// section) and the CLI exit-code function cmd/ composition roots call at the
// very end of main to turn any error into a process exit status.

// RPCError is the JSON-RPC 2.0 error object shape: {"code", "message",
// "data"}. D/S-06.T3 (the JSON-RPC framework) marshals this directly into an
// error envelope's "error" member. Plugin RPC (see PluginRPCError) reuses
// this exact shape and code table — the plan's "wire mapping onto JSON-RPC
// error envelopes, plugin RPC" is one table, not two.
type RPCError struct {
	// Code is the JSON-RPC application error code (codes.go), always one of
	// the RPCCode* constants for a taxonomy error, or RPCCodeInternal as the
	// fallback for a non-taxonomy error.
	Code int `json:"code"`
	// Message is the human-readable error message.
	Message string `json:"message"`
	// Data optionally carries structured detail. It is nil unless the
	// caller has a reason to attach one; this package never populates it on
	// its own.
	Data any `json:"data,omitempty"`
}

// Error implements the error interface so an *RPCError can itself be
// returned, logged, or wrapped like any other error.
func (e *RPCError) Error() string {
	return e.Message
}

// PluginRPCError is the plugin RPC error result type. It is a type alias
// for RPCError, not a distinct type, because A-T7's HOW section is explicit
// that "Plugin RPC reuses the JSON-RPC table verbatim" — the two protocols
// share one wire shape and one code table, so there is exactly one Go type
// to keep them from drifting apart.
type PluginRPCError = RPCError

// NewRPCError converts err into the JSON-RPC error envelope shape. When
// err's chain carries a taxonomy Kind (via KindOf), the envelope's Code is
// that Kind's JSONRPCCode and Message is err.Error(). Otherwise Code falls
// back to RPCCodeInternal — this is the one place a non-taxonomy error
// reaching a wire boundary is tolerated, precisely so the wire layer never
// panics or drops the message; the boundary lint (internal/build) is what
// keeps non-taxonomy errors from reaching here in the first place for
// pkg/cmd call sites. NewRPCError(nil) returns nil.
func NewRPCError(err error) *RPCError {
	if err == nil {
		return nil
	}
	kind, ok := KindOf(err)
	if !ok {
		return &RPCError{Code: RPCCodeInternal, Message: err.Error()}
	}
	return &RPCError{Code: kind.JSONRPCCode(), Message: err.Error()}
}

// NewPluginRPCError builds the plugin RPC error result for err. It is
// NewRPCError under a plugin-RPC-facing name; see PluginRPCError.
func NewPluginRPCError(err error) *PluginRPCError {
	return NewRPCError(err)
}

// ExitCode returns the CLI process exit status for err: ExitOK (0) when err
// is nil, the taxonomy Kind's ExitCode when err's chain carries one, and
// ExitInternal as the defensive fallback for any other non-nil error. cmd/
// composition roots call this exactly once, at the boundary between the
// command layer and os.Exit.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	kind, ok := KindOf(err)
	if !ok {
		return ExitInternal
	}
	return kind.ExitCode()
}
