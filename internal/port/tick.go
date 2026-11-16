package port

import (
	"context"
	"sync/atomic"
	"time"
)

// Tick drives idempotent reconciler passes with lease and work bounds.
type Tick struct {
	// WorkBound is the max items per pass.
	WorkBound int
	// LeaseDuration is how long the lease lasts.
	LeaseDuration time.Duration
	// AcquireLease obtains a lease before execution.
	AcquireLease func(ctx context.Context, d time.Duration) error
	// Journal returns resumable item keys.
	Journal func(ctx context.Context) ([]string, error)

	running atomic.Bool
}

// Run acquires a lease, fetches items, and calls fn with bounded items.
func (t *Tick) Run(ctx context.Context, fn func(ctx context.Context, items []string) error) error {
	if !t.running.CompareAndSwap(false, true) {
		return ErrConflict
	}
	defer t.running.Store(false)

	if err := t.AcquireLease(ctx, t.LeaseDuration); err != nil {
		return ErrUnavailable
	}

	items, err := t.Journal(ctx)
	if err != nil {
		return err
	}

	if len(items) > t.WorkBound {
		items = items[:t.WorkBound]
	}

	return fn(ctx, items)
}
