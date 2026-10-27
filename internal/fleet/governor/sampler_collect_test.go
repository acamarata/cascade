package governor

// Purpose: proves SamplerConfig.Collect replaces the platform collector and
// that a nil Collect falls back to it.
// SPORT: internal/fleet/governor.Sampler (ADD, P1-CAP-12 follow-up).

import (
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

func TestSamplerCollectOverride(t *testing.T) {
	want := ResourceSnapshot{MemTotalBytes: 42, MemUsedBytes: 7}
	clk := runtime.NewFixedClock(time.Unix(1000, 0))
	s := NewSampler(SamplerConfig{Collect: func() (ResourceSnapshot, error) { return want, nil }}, clk, newFakeTicker(), nil)
	s.tick()
	if got := s.Snapshot(); got.MemTotalBytes != 42 || got.MemUsedBytes != 7 || got.SampledAt.IsZero() {
		t.Fatalf("Snapshot = %+v, want the injected reading stamped by the clock", got)
	}
}

func TestSamplerCollectorFallsBackToPlatform(t *testing.T) {
	if (SamplerConfig{}).collector() == nil {
		t.Fatal("a nil Collect must resolve to the platform collector, not nil")
	}
}
