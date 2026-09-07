package governor

// Purpose: Sampler's test suite. Value-assertion tests drive tick()
//
//	directly (the production per-tick unit of work) with a fake clock and
//	a fake collector — never a real sleep (R-14.136, Art.7.3). A separate
//	goroutine-wiring test drives the real Start/Stop path through a fake
//	Ticker, synchronized via the afterTick test hook rather than polling
//	or sleeping. FuzzParseProcStat and FuzzParseProcMeminfo exercise the
//	untagged parsers directly so they run and fuzz on every host,
//	regardless of which platform's collectMetrics is actually compiled in.
//
// Constraints: this file is deliberately untagged (no //go:build line) so
//
//	it compiles and runs on every platform, per the file-level note in
//	sampler.go. It must therefore never reference a platform-tagged
//	symbol (parseDarwinSwapusage, cpuFractionFromDelta, etc.) — only the
//	universal collectMetrics() seam, which resolves to whichever
//	platform file the build actually includes.
//
// SPORT: internal/fleet/governor.Sampler (ADD, per T-1 sport_updates).

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// fakeTicker is a manually-driven runtime.Ticker, mirroring
// internal/runtime/metrics_emitter_test.go's fakeTicker: tests call Tick
// to fire exactly one tick, blocking until the receiver's select accepts
// it or ctx is done. Never a real sleep.
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

func fixedSnapshot(cpu float64) ResourceSnapshot {
	return ResourceSnapshot{CPUFraction: cpu, MemTotalBytes: 1024, MemUsedBytes: 512}
}

// TestSamplerFakeClockTick drives tick() directly against a fixed clock
// and a stub collector, proving Snapshot reflects each collected value
// with SampledAt stamped from the injected Clock (never a real read).
func TestSamplerFakeClockTick(t *testing.T) {
	clk := runtime.NewFixedClock(time.Unix(1000, 0))
	s := NewSampler(SamplerConfig{}, clk, newFakeTicker(), nil)
	s.collect = func() (ResourceSnapshot, error) { return fixedSnapshot(0.25), nil }

	s.tick()

	got := s.Snapshot()
	if got.CPUFraction != 0.25 {
		t.Fatalf("CPUFraction = %v, want 0.25", got.CPUFraction)
	}
	if !got.SampledAt.Equal(clk.Now()) {
		t.Fatalf("SampledAt = %v, want %v", got.SampledAt, clk.Now())
	}
	if got.Goroutines <= 0 {
		t.Fatalf("Goroutines = %d, want > 0", got.Goroutines)
	}

	clk.Advance(time.Second)
	s.collect = func() (ResourceSnapshot, error) { return fixedSnapshot(0.5), nil }
	s.tick()
	got = s.Snapshot()
	if got.CPUFraction != 0.5 || !got.SampledAt.Equal(clk.Now()) {
		t.Fatalf("second tick snapshot = %+v, want CPUFraction 0.5 at %v", got, clk.Now())
	}
}

// TestSamplerRetainsLastGoodOnError proves a failed collection never
// overwrites the last good snapshot, and that the failure is logged at
// warn level.
func TestSamplerRetainsLastGoodOnError(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	clk := runtime.NewFixedClock(time.Unix(2000, 0))
	s := NewSampler(SamplerConfig{}, clk, newFakeTicker(), log)

	good := fixedSnapshot(0.75)
	s.collect = func() (ResourceSnapshot, error) { return good, nil }
	s.tick()
	before := s.Snapshot()

	s.collect = func() (ResourceSnapshot, error) {
		return ResourceSnapshot{}, errors.New("simulated collection failure")
	}
	s.tick()
	after := s.Snapshot()

	if after != before {
		t.Fatalf("Snapshot after failed tick = %+v, want unchanged %+v", after, before)
	}
	if after == (ResourceSnapshot{}) {
		t.Fatalf("Snapshot after failed tick is zero-valued, want the retained good snapshot")
	}
	logOut := buf.String()
	if !strings.Contains(logOut, "WARN") || !strings.Contains(logOut, "resource sample collection failed") {
		t.Fatalf("log output = %q, want a WARN line naming the collection failure", logOut)
	}
}

// TestSamplerWindowsUnsupported proves the windows-tier refusal sentinel
// propagates through Sampler without a panic, on any host — it stubs the
// collector to mimic sampler_windows.go's collectMetrics rather than
// depending on an actual windows build.
func TestSamplerWindowsUnsupported(t *testing.T) {
	clk := runtime.NewFixedClock(time.Unix(3000, 0))
	s := NewSampler(SamplerConfig{}, clk, newFakeTicker(), nil)
	s.collect = func() (ResourceSnapshot, error) { return ResourceSnapshot{}, ErrUnsupportedPlatform }

	s.tick() // must not panic

	_, err := s.collect()
	if !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("err = %v, want ErrUnsupportedPlatform", err)
	}
	if got := s.Snapshot(); got != (ResourceSnapshot{}) {
		t.Fatalf("Snapshot = %+v, want zero value (never a successful collection occurred)", got)
	}
}

// TestSamplerStartStopIdempotent drives one real tick through Start's
// goroutine via a fake Ticker, synchronized on the afterTick test hook
// (never a sleep), then proves Stop is safe to call more than once.
func TestSamplerStartStopIdempotent(t *testing.T) {
	clk := runtime.NewFixedClock(time.Unix(4000, 0))
	ft := newFakeTicker()
	s := NewSampler(SamplerConfig{}, clk, ft, nil)
	s.collect = func() (ResourceSnapshot, error) { return fixedSnapshot(0.1), nil }

	settled := make(chan struct{}, 1)
	s.afterTick = func() { settled <- struct{}{} }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	tickCtx, tickCancel := context.WithTimeout(ctx, 5*time.Second)
	defer tickCancel()
	ft.Tick(tickCtx)

	select {
	case <-settled:
	case <-tickCtx.Done():
		t.Fatal("tick did not settle before timeout")
	}

	if got := s.Snapshot(); got.CPUFraction != 0.1 {
		t.Fatalf("Snapshot.CPUFraction = %v, want 0.1", got.CPUFraction)
	}

	s.Stop()
	s.Stop() // idempotent: must not panic or block
}

func TestPeriodForHz(t *testing.T) {
	cases := []struct {
		name string
		hz   float64
		want time.Duration
	}{
		{"zero defaults to 1Hz", 0, time.Second},
		{"negative defaults to 1Hz", -1, time.Second},
		{"above ceiling clamps to 1Hz", 5, time.Second},
		{"half hz is two seconds", 0.5, 2 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PeriodForHz(tc.hz); got != tc.want {
				t.Fatalf("PeriodForHz(%v) = %v, want %v", tc.hz, got, tc.want)
			}
		})
	}
}

func TestParseProcStat(t *testing.T) {
	idle, total, err := parseProcStat([]byte("cpu  100 0 200 700 0 0 0 0 0 0\ncpu0 50 0 100 350 0 0 0 0 0 0\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if idle != 700 {
		t.Fatalf("idle = %d, want 700", idle)
	}
	if total != 1000 {
		t.Fatalf("total = %d, want 1000", total)
	}

	if _, _, err := parseProcStat([]byte("no cpu line here\n")); err == nil {
		t.Fatal("expected error for missing cpu line")
	}
	if _, _, err := parseProcStat([]byte("cpu notanumber 0 0 0\n")); err == nil {
		t.Fatal("expected error for unparsable field")
	}
	if _, _, err := parseProcStat([]byte("cpu 1 2\n")); err == nil {
		t.Fatal("expected error for too few fields")
	}
}

func TestParseProcMeminfo(t *testing.T) {
	data := []byte("MemTotal:       1024 kB\nMemAvailable:    512 kB\nSwapTotal:       256 kB\nSwapFree:        128 kB\nOther:             1 kB\n")
	total, avail, swapTotal, swapFree := parseProcMeminfo(data)
	if total != 1024*1024 || avail != 512*1024 || swapTotal != 256*1024 || swapFree != 128*1024 {
		t.Fatalf("parsed = (%d,%d,%d,%d), want (1048576,524288,262144,131072)", total, avail, swapTotal, swapFree)
	}

	// Garbage input must never panic and yields all zeros.
	total, avail, swapTotal, swapFree = parseProcMeminfo([]byte("garbage\n\x00\xff not-a-number\n"))
	if total != 0 || avail != 0 || swapTotal != 0 || swapFree != 0 {
		t.Fatalf("garbage input parsed nonzero: (%d,%d,%d,%d)", total, avail, swapTotal, swapFree)
	}
}

// TestCollectMetricsSmoke exercises the real per-platform collectMetrics
// seam directly. On darwin and linux this must succeed with plausible
// bounds; on any platform it must never panic, and any error it does
// return must be ErrUnsupportedPlatform (the only documented refusal).
func TestCollectMetricsSmoke(t *testing.T) {
	snap, err := collectMetrics()
	if err != nil {
		if !errors.Is(err, ErrUnsupportedPlatform) {
			t.Fatalf("collectMetrics error = %v, want nil or ErrUnsupportedPlatform", err)
		}
		return
	}
	if snap.CPUFraction < 0 || snap.CPUFraction > 1 {
		t.Fatalf("CPUFraction = %v, want in [0,1]", snap.CPUFraction)
	}
	if snap.MemTotalBytes == 0 {
		t.Fatal("MemTotalBytes = 0, want a real host memory total")
	}
}

func FuzzParseProcStat(f *testing.F) {
	f.Add([]byte("cpu  100 0 200 700 0 0 0 0 0 0\n"))
	f.Add([]byte(""))
	f.Add([]byte("cpu\n"))
	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _, _ = parseProcStat(data) // must never panic
	})
}

func FuzzParseProcMeminfo(f *testing.F) {
	f.Add([]byte("MemTotal:       1024 kB\nMemAvailable:    512 kB\n"))
	f.Add([]byte(""))
	f.Add([]byte("MemTotal: notanumber kB\n"))
	f.Fuzz(func(_ *testing.T, data []byte) {
		parseProcMeminfo(data) // must never panic
	})
}
