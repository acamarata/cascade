package write

import (
	"context"
	"errors"
)

// Handler is a function that processes a routed operation payload.
type Handler func(ctx context.Context, payload []byte) error

// Handlers is a map of operation names to their corresponding handlers.
type Handlers map[string]Handler

// ErrUnknownOp is returned when the operation is unknown, empty, or padded,
// when the handlers map is nil or empty, or when the selected handler is missing or nil.
var ErrUnknownOp = errors.New("E_UNKNOWN_OP")

// ErrInvalidRoute is returned when a valid operation selection is made but the context is nil.
var ErrInvalidRoute = errors.New("invalid route context")

var supportedOps = map[string]bool{
	"hook-run":  true,
	"shelf":     true,
	"sysone":    true,
	"decide":    true,
	"hook-dist": true,
	"flow-run":  true,
}

// Route dispatches the payload to the handler associated with the given operation.
// It returns ErrUnknownOp for unknown, empty, or padded operations, nil/empty maps,
// or missing/nil handlers. It returns ErrInvalidRoute if the operation is valid
// but the context is nil. Extra keys in the handlers map are ignored.
func Route(ctx context.Context, op string, payload []byte, handlers Handlers) error {
	if !supportedOps[op] {
		return ErrUnknownOp
	}
	if handlers == nil {
		return ErrUnknownOp
	}
	handler, ok := handlers[op]
	if !ok || handler == nil {
		return ErrUnknownOp
	}
	if ctx == nil {
		return ErrInvalidRoute
	}
	return handler(ctx, payload)
}
