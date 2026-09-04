// Purpose: the regression proof for the shutdown race that left the
//   advisory lease written after a graceful daemon shutdown, so the next
//   daemon process failed Activate with a KindConflict for the rest of
//   the lease. At the CLI that surfaced as `cascade daemon restart`
//   reporting "socket never became ready": the replacement daemon was
//   spawned fine and then died at startup on the stale lease.
// SPORT: internal.events.scheduler.Scheduler/CHANGED (Close barrier).

package scheduler

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/provider"
)

// gatedStore delays the first Tx made after Arm, so a test can hold one
// goroutine inside the lease Release and observe what a second one does
// meanwhile. Every other method is the wrapped store's.
type gatedStore struct {
	provider.Store
	armed   chan struct{}
	entered chan struct{}
	release chan struct{}
}

func newGatedStore(inner provider.Store) *gatedStore {
	return &gatedStore{
		Store:   inner,
		armed:   make(chan struct{}),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

// Arm makes the next Tx block until Release is called.
func (g *gatedStore) Arm() { close(g.armed) }

// Release lets the gated Tx proceed.
func (g *gatedStore) Release() { close(g.release) }

func (g *gatedStore) Tx(ctx context.Context, fn func(context.Context, provider.Tx) error) error {
	select {
	case <-g.armed:
		select {
		case <-g.entered:
			// A later Tx: the gate fires exactly once.
		default:
			close(g.entered)
			<-g.release
		}
	default:
	}
	return g.Store.Tx(ctx, fn)
}

// yieldRounds bounds the cooperative yielding the assertion below does
// instead of sleeping (R-14.136 forbids a bare Sleep): enough scheduler
// turns that an unbarriered Close would certainly have returned, with no
// wall-clock dependency.
const yieldRounds = 1000

// TestSchedulerClose_BlocksUntilTheLeaseReleaseLands proves Close's
// contract at the point the daemon's shutdown depends on it: when Close
// returns, the lease is gone, not merely being removed by some other
// goroutine.
//
// The daemon's cleanup cancels the scheduler context and then calls
// Close, treating its return as "shutdown complete" before the process
// exits. Cancelling starts watchCancellation's own Close, which takes the
// active flag and then spends a Store round trip inside the lease
// Release. Without the closeMu barrier the explicit Close saw active
// already false and returned nil immediately, so the process could exit
// with the lease still written, and the next daemon could not Activate.
func TestSchedulerClose_BlocksUntilTheLeaseReleaseLands(t *testing.T) {
	inner := storetest.NewMemStore()
	store := newGatedStore(inner)
	clock := testkit.NewFrozenClock(testEpoch)
	bus := events.New(inner, clock)
	holder := New(store, testNamespace, clock, bus, "owner-a", time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	if _, err := holder.Activate(ctx); err != nil {
		t.Fatalf("holder.Activate: %v", err)
	}

	// From here the next Tx (the lease Release) is held open.
	store.Arm()
	cancel()
	<-store.entered

	closed := make(chan error, 1)
	go func() { closed <- holder.Close(context.Background()) }()

	for i := 0; i < yieldRounds; i++ {
		runtime.Gosched()
	}
	select {
	case err := <-closed:
		t.Fatalf("Close returned (%v) while the lease Release was still in flight; a caller that exits the process on that return leaves the lease written", err)
	default:
	}

	store.Release()
	if err := <-closed; err != nil {
		t.Fatalf("holder.Close: %v", err)
	}

	// The successor is what the released lease is FOR: the next daemon
	// process must be able to Activate.
	successor := New(inner, testNamespace, clock, bus, "owner-b", time.Hour)
	if _, err := successor.Activate(context.Background()); err != nil {
		t.Fatalf("successor.Activate after a completed Close = %v, want success (lease released)", err)
	}
	if err := successor.Close(context.Background()); err != nil {
		t.Fatalf("successor.Close: %v", err)
	}
}
