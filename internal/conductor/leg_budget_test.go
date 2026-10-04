package conductor

// Purpose: unit coverage for NewLegBudget (leg_budget.go): the slot bound
//   holds under concurrency, a waiter whose ctx ends never runs fn, and a
//   non-positive size still bounds to one slot.
// SPORT: conductor.fanout/CHANGE (P1-CORE-19).

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestLegBudgetBoundsConcurrency(t *testing.T) {
	for _, n := range []int{0, 1, 3} {
		budget, want := NewLegBudget(n), int32(max(n, 1))
		var inflight, peak atomic.Int32
		var wg sync.WaitGroup
		release := make(chan struct{})
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = budget(context.Background(), func(context.Context) error {
					cur := inflight.Add(1)
					for m := peak.Load(); cur > m && !peak.CompareAndSwap(m, cur); {
						m = peak.Load()
					}
					<-release
					inflight.Add(-1)
					return nil
				})
			}()
		}
		close(release)
		wg.Wait()
		if peak.Load() > want {
			t.Fatalf("n=%d: peak %d legs in flight, want <= %d", n, peak.Load(), want)
		}
	}
}

func TestLegBudgetCancelledWaiterNeverRuns(t *testing.T) {
	budget := NewLegBudget(1)
	held, done := make(chan struct{}), make(chan struct{})
	go func() {
		_ = budget(context.Background(), func(context.Context) error { close(held); <-done; return nil })
	}()
	<-held
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ran := false
	err := budget(ctx, func(context.Context) error { ran = true; return nil })
	if !errors.Is(err, context.Canceled) || ran {
		t.Fatalf("waiter = (%v, ran=%v), want context.Canceled and fn never run", err, ran)
	}
	close(done)
	if err := budget(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("slot not released after the holder returned: %v", err)
	}
}
