// Package supervise provides supervisor management for processes.
package supervise

import (
	"context"
	"errors"
)

// ErrNotImplemented is returned when a supervise operation is not available.
var ErrNotImplemented = errors.New("supervise: operation unavailable")

// Supervisor manages supervised processes.
type Supervisor struct{}

// Stop stops the supervisor. It returns ErrNotImplemented as a stub.
func Stop(_ context.Context, _ *Supervisor) error {
	return ErrNotImplemented
}

// Restart restarts the supervisor. It returns ErrNotImplemented as a stub.
func Restart(_ context.Context, _ *Supervisor) error {
	return ErrNotImplemented
}
