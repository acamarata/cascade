package port

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// ErrTickOverlapping is returned when Run is invoked while another Run is in progress.
var ErrTickOverlapping = errors.New("tick: overlapping execution")

// Tick coordinates periodic work passes with a journal resume and bounded batch sizes.
type Tick struct {
	// MaxItems is the maximum number of items passed to the work function per pass.
	MaxItems int
	// LeaseDuration is the duration for which the lease is held.
	LeaseDuration time.Duration
	// Journal returns resumable item keys for the current pass.
	Journal func(ctx context.Context) ([]string, error)

	running int32
}

// Run executes a single pass of work. It acquires a lease, ensuring no overlapping
// execution. If the lease cannot be acquired, it returns an error immediately.
// It calls the Journal callback to get item keys, bounds the slice to MaxItems,
// and invokes fn with the resulting items.
func (t *Tick) Run(ctx context.Context, fn func(ctx context.Context, items []string) error) error {
	if !atomic.CompareAndSwapInt32(&t.running, 0, 1) {
		return ErrTickOverlapping
	}
	defer atomic.StoreInt32(&t.running, 0)

	if t.Journal == nil {
		return errors.New("tick: journal callback is nil")
	}

	items, err := t.Journal(ctx)
	if err != nil {
		return err
	}

	if t.MaxItems > 0 && len(items) > t.MaxItems {
		items = items[:t.MaxItems]
	}

	return fn(ctx, items)
}
