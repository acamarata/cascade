package fleet

// Purpose: HeadroomPublisher's goroutine-wiring suite: the real Start/Stop
//   tick loop driven through a fake Ticker and synchronized via the
//   afterTick test hook (never a sleep, never a bare <-ch - Art.7.3), the
//   C-S05.T4 Registry-publication proof, the -race concurrent-mutation
//   proof, and the A-T3 bench-budget registration (S-40.T2 task 5). Value-
//   assertion tests for Compute() itself live in headroom_test.go (300-
//   line cap split, same ticket, same package).
// SPORT: internal/fleet.HeadroomModel (ADD, per T-2 sport_updates).

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/build"
	"github.com/acamarata/cascade/internal/runtime"
)

// TestHeadroomPublishesToRegistrySnapshotAPI proves the C-S05.T4 gauge
// snapshot API is the publication surface: a downstream consumer reads
// published headroom via *runtime.Registry without importing internal/fleet.
func TestHeadroomPublishesToRegistrySnapshotAPI(t *testing.T) {
	adm := &fakeAdmission{value: 1}
	ceil := &fakeCeiling{value: 4}
	reg := runtime.NewRegistry()
	clk := runtime.NewFixedClock(time.Unix(5, 0))
	ft := newFakeTicker()
	p := NewHeadroomPublisher(adm, ceil, nil, reg, clk, ft)

	settled := make(chan struct{}, 1)
	p.afterTick = func() { settled <- struct{}{} }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	tickCtx, tickCancel := context.WithTimeout(ctx, 5*time.Second)
	defer tickCancel()
	ft.Tick(tickCtx)

	select {
	case <-settled:
	case <-tickCtx.Done():
		t.Fatal("tick did not settle before timeout")
	}
	p.Stop()

	samples := reg.Snapshot(clk.Now())
	found := false
	for _, s := range samples {
		if s.Name == "fleet_headroom_ratio_milli" {
			found = true
			if s.Value != 750 {
				t.Fatalf("fleet_headroom_ratio_milli = %d, want 750 (0.75 ratio)", s.Value)
			}
		}
	}
	if !found {
		t.Fatal("fleet_headroom_ratio_milli gauge not found in Registry.Snapshot")
	}
}

// TestHeadroomStartStopIdempotent drives one real tick through Start's
// goroutine, then proves Stop is safe to call more than once.
func TestHeadroomStartStopIdempotent(t *testing.T) {
	adm := &fakeAdmission{value: 0}
	ceil := &fakeCeiling{value: 4}
	ft := newFakeTicker()
	p := NewHeadroomPublisher(adm, ceil, nil, nil, runtime.NewFixedClock(time.Unix(1, 0)), ft)

	settled := make(chan struct{}, 1)
	p.afterTick = func() { settled <- struct{}{} }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	tickCtx, tickCancel := context.WithTimeout(ctx, 5*time.Second)
	defer tickCancel()
	ft.Tick(tickCtx)

	select {
	case <-settled:
	case <-tickCtx.Done():
		t.Fatal("tick did not settle before timeout")
	}

	p.Stop()
	p.Stop() // idempotent: must not panic or block
}

// TestHeadroomUnavailableTickLeavesGaugesUnchanged proves that a tick
// hitting ErrHeadroomUnavailable does not overwrite gauges with a
// misleading zero.
func TestHeadroomUnavailableTickLeavesGaugesUnchanged(t *testing.T) {
	adm := &fakeAdmission{value: 1}
	ceil := &fakeCeiling{value: 4}
	reg := runtime.NewRegistry()
	ft := newFakeTicker()
	p := NewHeadroomPublisher(adm, ceil, nil, reg, runtime.NewFixedClock(time.Unix(1, 0)), ft)

	settled := make(chan struct{}, 1)
	p.afterTick = func() { settled <- struct{}{} }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	tickCtx, tickCancel := context.WithTimeout(ctx, 5*time.Second)
	defer tickCancel()
	ft.Tick(tickCtx)
	select {
	case <-settled:
	case <-tickCtx.Done():
		t.Fatal("first tick did not settle before timeout")
	}

	ceil.set(0) // now unavailable
	ft.Tick(tickCtx)
	select {
	case <-settled:
	case <-tickCtx.Done():
		t.Fatal("second tick did not settle before timeout")
	}
	p.Stop()

	got, ok := reg.Get("fleet_headroom_enforced_ceiling")
	if !ok {
		t.Fatal("fleet_headroom_enforced_ceiling gauge missing")
	}
	if got.Value() != 4 {
		t.Fatalf("fleet_headroom_enforced_ceiling = %d, want 4 (unchanged from the last good tick)", got.Value())
	}
}

// TestHeadroomRaceConcurrentMutationAndReads proves permit-adjacent state
// (the fake AdmissionState's mutated value) races cleanly against
// concurrent Compute reads under -race.
func TestHeadroomRaceConcurrentMutationAndReads(t *testing.T) {
	t.Helper()
	adm := &fakeAdmission{value: 1}
	ceil := &fakeCeiling{value: 8}
	p := NewHeadroomPublisher(adm, ceil, nil, nil, runtime.NewFixedClock(time.Unix(1, 0)), newFakeTicker())

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				adm.set(i % 8)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = p.Compute()
			}
		}
	}()
	time.Sleep(20 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// BenchmarkHeadroomCompute registers the A-T3 bench-budget entry for
// headroom recomputation latency (S-40.T2 task 5); headroomBenchBudget's
// zero-valued fields mean "no ceiling asserted yet" per
// internal/build.AssertBudgets's contract - the numeric ceiling is
// AB/S-58.T2's to assign.
var headroomBenchBudget = build.Budget{Name: "HeadroomCompute"}

func BenchmarkHeadroomCompute(b *testing.B) {
	_ = headroomBenchBudget
	adm := &fakeAdmission{value: 2}
	ceil := &fakeCeiling{value: 8}
	p := NewHeadroomPublisher(adm, ceil, nil, nil, runtime.NewFixedClock(time.Unix(1, 0)), newFakeTicker())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = p.Compute()
	}
}
