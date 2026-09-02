package runtime

import (
	"context"
	goruntime "runtime"
	"testing"
	"time"
)

// Purpose: Bootstrap's metrics-wiring proof (T-4 task 6) — the Registry is
//   always available for subsystem registration, and the periodic
//   emitter starts if and only if BootstrapOptions.MetricsBus is set.
//   Split out of metrics_test.go under R-14.117 (Art.10.3 cap).
// Inputs: none from outside.
// Outputs: n/a (test file).
// Constraints: Art.7.1 — every Bootstrap call below uses t.TempDir() as
//   HomeDir, never a real home directory.
// SPORT: runtime/metrics (ADD, placeholder per T-1 sport_updates);
//   runtime/bootstrap (existing, change per T-1 sport_updates).

// --- Bootstrap wiring ---------------------------------------------------

// TestBootstrap_MetricsRegistryAlwaysAvailable proves Bootstrap always
// creates a usable Registry, and that a subsystem can register a counter
// and a gauge against it in the bootstrap sequence (T-4 task 6).
func TestBootstrap_MetricsRegistryAlwaysAvailable(t *testing.T) {
	home := t.TempDir()
	rt, err := Bootstrap(context.Background(), BootstrapOptions{
		Getenv:  func(string) string { return "" },
		HomeDir: func() (string, error) { return home, nil },
		Environ: fakeEnviron(nil),
		Clock:   NewFixedClock(time.Unix(0, 0)),
	})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if rt.Metrics == nil {
		t.Fatal("Runtime.Metrics = nil, want an always-available Registry")
	}

	c := rt.Metrics.RegisterCounter("bootstrap_wiring_test_total", nil)
	g := rt.Metrics.RegisterGauge("bootstrap_wiring_test_gauge", nil)
	c.Inc()
	g.Set(7)

	samples := rt.Metrics.Snapshot(rt.Clock.Now())
	if len(samples) != 2 {
		t.Fatalf("Snapshot() after registering in bootstrap sequence = %d samples, want 2", len(samples))
	}
}

// TestBootstrap_NoMetricsBusStartsNoEmitter proves the honest default: with
// MetricsBus left nil (as every current caller leaves it — no composition
// root wires a real bus yet), Bootstrap creates the Registry but starts no
// emitter goroutine and touches no Bus.
func TestBootstrap_NoMetricsBusStartsNoEmitter(t *testing.T) {
	home := t.TempDir()
	before := goruntime.NumGoroutine()
	rt, err := Bootstrap(context.Background(), BootstrapOptions{
		Getenv:  func(string) string { return "" },
		HomeDir: func() (string, error) { return home, nil },
		Environ: fakeEnviron(nil),
		Clock:   NewFixedClock(time.Unix(0, 0)),
	})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if rt.Metrics == nil {
		t.Fatal("Runtime.Metrics = nil even with no MetricsBus")
	}
	// Give any wrongly-started goroutine a chance to appear.
	for i := 0; i < 1000; i++ {
		goruntime.Gosched()
	}
	if after := goruntime.NumGoroutine(); after > before+1 {
		t.Errorf("goroutine count = %d (before %d) with no MetricsBus set — an emitter goroutine must not start", after, before)
	}
}

// TestBootstrap_MetricsBusStartsEmitter proves that supplying MetricsBus
// makes Bootstrap actually start the periodic emitter, using a short
// MetricsInterval against the real NewSystemTicker (production path) and
// polling — never sleeping as the synchronization primitive itself — for
// the first publish.
func TestBootstrap_MetricsBusStartsEmitter(t *testing.T) {
	home := t.TempDir()
	bus := &fakeBus{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rt, err := Bootstrap(ctx, BootstrapOptions{
		Getenv:          func(string) string { return "" },
		HomeDir:         func() (string, error) { return home, nil },
		Environ:         fakeEnviron(nil),
		Clock:           NewFixedClock(time.Unix(0, 0)),
		MetricsBus:      bus,
		MetricsInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	rt.Metrics.RegisterCounter("wired_total", nil)

	deadline := time.After(500 * time.Millisecond)
	for bus.count() == 0 {
		select {
		case <-deadline:
			t.Fatal("Bootstrap with MetricsBus set published nothing within 500ms of a 1ms interval")
		default:
			goruntime.Gosched()
		}
	}
}
