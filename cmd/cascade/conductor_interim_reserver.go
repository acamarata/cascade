//go:build !windows

// Purpose: the interim call reserver the daemon binds into its conductor
//   accounting until the fleet dispatch wiring installs the real one.
// Inputs: none. Outputs: a conductor.CallReserver that admits every call.
// Constraints: it reproduces the unreserved behaviour of the base, where no
//   dispatch held a reservation. It enforces no capacity; the fleet dispatch
//   wiring replaces it. The conductor itself stays fail-closed: a nil
//   Reserver still refuses every dispatch.

package main

import (
	"context"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/pkg/provider"
)

// interimReserver admits every dispatch with a hold that runs the call and
// releases nothing. It keeps the daemon's dispatch behaviour as it was
// before calls were reserved; the fleet dispatch wiring replaces it.
type interimReserver struct{}

// Reserve admits an unreserved dispatch.
func (interimReserver) Reserve(context.Context, provider.Selection, provider.ModelRequest) (conductor.HeldCall, error) {
	return interimHold{id: "interim"}, nil
}

// Check admits a named reservation unchecked, as the base did.
func (interimReserver) Check(_ context.Context, id string, _ string, _ provider.Selection) (conductor.HeldCall, error) {
	return interimHold{id: id}, nil
}

// interimHold is the no-op hold interimReserver hands out.
type interimHold struct{ id string }

// ID returns the hold's reservation id.
func (h interimHold) ID() string { return h.id }

// WithPermit runs fn with no permit held.
func (interimHold) WithPermit(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

// Release is a no-op: there is nothing held to return.
func (interimHold) Release(context.Context, provider.Usage, bool) error { return nil }
