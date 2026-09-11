package fleet

// Purpose: HeadroomPublisher's Compute() value-assertion suite: fake
//   AdmissionState/CeilingSource/census.Reader seams, driven directly
//   (never a real Admit call, never a real sleep - Art.7.3). The
//   goroutine-wiring (Start/Stop/Registry publication), the concurrent
//   -race proof, and the A-T3 bench-budget registration live in
//   headroom_publish_test.go (300-line cap split, same ticket, same
//   package).
// SPORT: internal/fleet.HeadroomModel (ADD, per T-2 sport_updates).

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/census"
	"github.com/acamarata/cascade/internal/runtime"
)

// fakeAdmission and fakeCeiling let tests drive HeadroomPublisher's two
// seams independently of any real governor.AdmissionController, and
// mutate concurrently under -race.
type fakeAdmission struct {
	mu    sync.Mutex
	value int
}

func (f *fakeAdmission) set(v int) { f.mu.Lock(); f.value = v; f.mu.Unlock() }
func (f *fakeAdmission) Inflight() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.value
}

type fakeCeiling struct {
	mu    sync.Mutex
	value int
}

func (f *fakeCeiling) set(v int) { f.mu.Lock(); f.value = v; f.mu.Unlock() }
func (f *fakeCeiling) EnforcedCeiling() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.value
}

// fakeTicker is a manually-driven runtime.Ticker, mirroring
// internal/fleet/governor/sampler_test.go's fakeTicker: Tick fires exactly
// one tick, blocking until the receiver's select accepts it or ctx is
// done. Never a real sleep.
type fakeTicker struct {
	c        chan struct{}
	stopOnce sync.Once
}

func newFakeTicker() *fakeTicker { return &fakeTicker{c: make(chan struct{})} }

func (f *fakeTicker) C() <-chan struct{} { return f.c }
func (f *fakeTicker) Stop()              { f.stopOnce.Do(func() {}) }

func (f *fakeTicker) Tick(ctx context.Context) {
	select {
	case f.c <- struct{}{}:
	case <-ctx.Done():
	}
}

func TestHeadroomComputeFullCapacityReturnsZeroRatio(t *testing.T) {
	adm := &fakeAdmission{value: 4}
	ceil := &fakeCeiling{value: 4}
	p := NewHeadroomPublisher(adm, ceil, nil, nil, runtime.NewFixedClock(time.Unix(1, 0)), newFakeTicker())

	h, err := p.Compute()
	if err != nil {
		t.Fatalf("Compute() error = %v, want nil", err)
	}
	if h.Ratio != 0.0 {
		t.Fatalf("Ratio = %v, want 0.0 at full capacity (admitted==enforced_ceiling)", h.Ratio)
	}
	if h.Admitted != 4 || h.EnforcedCeiling != 4 {
		t.Fatalf("Admitted/EnforcedCeiling = %d/%d, want 4/4", h.Admitted, h.EnforcedCeiling)
	}
}

func TestHeadroomComputeZeroAdmissionIsFullHeadroom(t *testing.T) {
	adm := &fakeAdmission{value: 0}
	ceil := &fakeCeiling{value: 8}
	p := NewHeadroomPublisher(adm, ceil, nil, nil, runtime.NewFixedClock(time.Unix(1, 0)), newFakeTicker())

	h, err := p.Compute()
	if err != nil {
		t.Fatalf("Compute() error = %v, want nil", err)
	}
	if h.Ratio != 1.0 {
		t.Fatalf("Ratio = %v, want 1.0 at zero admission", h.Ratio)
	}
}

func TestHeadroomComputeEnforcedCeilingZeroRefused(t *testing.T) {
	adm := &fakeAdmission{value: 0}
	ceil := &fakeCeiling{value: 0}
	p := NewHeadroomPublisher(adm, ceil, nil, nil, runtime.NewFixedClock(time.Unix(1, 0)), newFakeTicker())

	_, err := p.Compute()
	if err == nil {
		t.Fatal("Compute() error = nil, want ErrHeadroomUnavailable on enforced_ceiling=0 (no division by zero)")
	}
}

// TestHeadroomComputeMissingSeamsRefusedNotUnlimited proves the "unknown
// capacity signal never reads as plenty" trap directly: a nil seam refuses
// rather than defaulting to some permissive ratio.
func TestHeadroomComputeMissingSeamsRefusedNotUnlimited(t *testing.T) {
	p := NewHeadroomPublisher(nil, nil, nil, nil, runtime.NewFixedClock(time.Unix(1, 0)), newFakeTicker())
	_, err := p.Compute()
	if err == nil {
		t.Fatal("Compute() error = nil, want ErrHeadroomUnavailable with nil seams (missing signal must never read as unlimited)")
	}
}

func TestHeadroomComputeCeilingDropsMidRun(t *testing.T) {
	adm := &fakeAdmission{value: 3}
	ceil := &fakeCeiling{value: 10}
	p := NewHeadroomPublisher(adm, ceil, nil, nil, runtime.NewFixedClock(time.Unix(1, 0)), newFakeTicker())

	h1, err := p.Compute()
	if err != nil {
		t.Fatalf("Compute() error = %v", err)
	}
	if h1.Ratio != 0.7 {
		t.Fatalf("Ratio = %v, want 0.7 at 3/10", h1.Ratio)
	}

	ceil.set(4) // throttle ladder drops the ceiling mid-run
	h2, err := p.Compute()
	if err != nil {
		t.Fatalf("Compute() error = %v", err)
	}
	if h2.EnforcedCeiling != 4 {
		t.Fatalf("EnforcedCeiling = %d, want 4 (mid-run ceiling drop reflected without daemon restart)", h2.EnforcedCeiling)
	}
	if h2.Ratio != 0.25 {
		t.Fatalf("Ratio = %v, want 0.25 at 3/4 after the ceiling dropped", h2.Ratio)
	}
}

// TestHeadroomConfigMaxNeverDenominates proves binding-ceiling honesty
// directly: EnforcedCeiling, not a larger static config maximum, is
// always the denominator.
func TestHeadroomConfigMaxNeverDenominates(t *testing.T) {
	const staticConfigMax = 100
	adm := &fakeAdmission{value: 2}
	ceil := &fakeCeiling{value: 4} // throttle ladder has bound this far below staticConfigMax
	p := NewHeadroomPublisher(adm, ceil, nil, nil, runtime.NewFixedClock(time.Unix(1, 0)), newFakeTicker())

	h, err := p.Compute()
	if err != nil {
		t.Fatalf("Compute() error = %v", err)
	}
	if h.EnforcedCeiling == staticConfigMax {
		t.Fatal("EnforcedCeiling equals the static config max; binding-ceiling honesty requires the live throttle ceiling")
	}
	if h.EnforcedCeiling != 4 {
		t.Fatalf("EnforcedCeiling = %d, want 4 (the live enforced ceiling, not %d)", h.EnforcedCeiling, staticConfigMax)
	}
}

// TestHeadroomConsumesCensusReaderOnly proves census state reaches
// Headroom.CensusCount only through the census.Reader interface (Count),
// never a concrete Collector's internals.
func TestHeadroomConsumesCensusReaderOnly(t *testing.T) {
	coll := census.NewCollector()
	coll.Store([]census.Snapshot{{}, {}, {}})
	var reader census.Reader = coll // seam-typed, not *census.Collector

	adm := &fakeAdmission{value: 1}
	ceil := &fakeCeiling{value: 2}
	p := NewHeadroomPublisher(adm, ceil, reader, nil, runtime.NewFixedClock(time.Unix(1, 0)), newFakeTicker())

	h, err := p.Compute()
	if err != nil {
		t.Fatalf("Compute() error = %v", err)
	}
	if h.CensusCount != 3 {
		t.Fatalf("CensusCount = %d, want 3 via census.Reader.Count()", h.CensusCount)
	}
}
