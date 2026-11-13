package daemon

// Purpose: the pass-through call reserver the daemon test fixtures bind; production binds no such default.

import (
	"context"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/pkg/provider"
)

// passReserver is the shared daemon test reserver: every dispatch gets a
// hold that runs fn and releases nothing. Production has no such default.
type passReserver struct{}

func (passReserver) Reserve(context.Context, provider.Selection, provider.ModelRequest) (conductor.HeldCall, error) {
	return passHold{id: "pass"}, nil
}

func (passReserver) Check(_ context.Context, id string, _ string, _ provider.Selection) (conductor.HeldCall, error) {
	return passHold{id: id}, nil
}

type passHold struct{ id string }

func (h passHold) ID() string { return h.id }

func (passHold) WithPermit(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

func (passHold) Release(context.Context, provider.Usage, bool) error { return nil }
