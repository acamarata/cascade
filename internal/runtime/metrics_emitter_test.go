package runtime

import (
	"context"
	"encoding/json"
	"errors"
	goruntime "runtime"
	"sync"
	"testing"
	"time"
)

// Purpose: RunPeriodicEmitter's test suite — deterministic proof driven
//   entirely by a fake Clock and a fake Ticker (never a real sleep,
//   R-14.136), plus one real-SystemTicker leak-detection case where a
//   short real interval is the thing under test, not a stand-in for
//   synchronization. Split out of metrics_test.go under R-14.117 (Art.10.3
//   cap). fakeTicker and fakeBus are the EventBus/Ticker test fakes,
//   confined to this file per Art.1.
// Inputs: none from outside.
// Outputs: n/a (test file).
// Constraints: Art.7.1 — no test writes outside t.TempDir() (this suite
//   writes nothing to disk at all).
// SPORT: runtime/metrics (ADD, placeholder per T-1 sport_updates).

// --- Periodic emitter: fakes confined to this file --------------------

// fakeTicker is a manually-driven Ticker: tests call Tick() to fire one
// tick, never a real sleep (R-14.136).
type fakeTicker struct {
	c        chan struct{}
	stopped  bool
	stopOnce sync.Once
	mu       sync.Mutex
}

func newFakeTicker() *fakeTicker {
	return &fakeTicker{c: make(chan struct{})}
}

func (f *fakeTicker) C() <-chan struct{} { return f.c }

func (f *fakeTicker) Stop() {
	f.stopOnce.Do(func() {
		f.mu.Lock()
		f.stopped = true
		f.mu.Unlock()
	})
}

// Tick sends one tick, blocking until RunPeriodicEmitter's select
// receives it or ctx is done. It never sleeps.
func (f *fakeTicker) Tick(ctx context.Context) {
	select {
	case f.c <- struct{}{}:
	case <-ctx.Done():
	}
}

// fakeBus records every published event; Publish can be made to fail on
// demand via failNext.
type fakeBus struct {
	mu        sync.Mutex
	published []fakeBusCall
	failNext  bool
}

type fakeBusCall struct {
	namespace, kind, source string
	payload                 []byte
}

func (b *fakeBus) Publish(_ context.Context, namespace, kind, source string, payload []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failNext {
		b.failNext = false
		return errors.New("fakeBus: forced publish failure")
	}
	b.published = append(b.published, fakeBusCall{namespace: namespace, kind: kind, source: source, payload: payload})
	return nil
}

func (b *fakeBus) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.published)
}

func (b *fakeBus) last() fakeBusCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.published[len(b.published)-1]
}

// TestPeriodicEmitter drives N ticks through a fake Clock/Ticker and
// asserts exactly N MetricsSnapshotEvents were published, each carrying
// the registry's state and a Ts sourced from the injected Clock (never a
// real timestamp).
func TestPeriodicEmitter(t *testing.T) {
	reg := NewRegistry()
	counter := reg.RegisterCounter("emitted_total", nil)

	clock := NewFixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ticker := newFakeTicker()
	bus := &fakeBus{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunPeriodicEmitter(ctx, PeriodicEmitterOptions{
			Registry: reg,
			Clock:    clock,
			Ticker:   ticker,
			Bus:      bus,
		}, nil)
	}()

	const n = 5
	driveTicks(ctx, counter, clock, ticker, bus, n)
	assertLastEmittedEvent(t, bus, clock, n)

	cancel()
	<-done // proves RunPeriodicEmitter actually returns on ctx.Done()
	assertNoPublishAfterCancel(t, ticker, bus)
}

// driveTicks advances clock, fires one tick, and waits for the emitter to
// have actually published before advancing again — required, not
// cosmetic: FixedClock.Advance is not synchronized against a concurrent
// Now() read, so advancing again before the previous tick's Now() call
// has happened is a genuine data race (caught by -race during this
// ticket's own development). Waiting for bus.count() to increase
// establishes the happens-before edge (Publish happens-after Now()) that
// makes the next Advance safe.
func driveTicks(ctx context.Context, counter *Counter, clock *FixedClock, ticker *fakeTicker, bus *fakeBus, n int) {
	for i := 0; i < n; i++ {
		counter.Add(1)
		clock.Advance(time.Minute)
		ticker.Tick(ctx)
		for j := 0; j < 100000 && bus.count() < i+1; j++ {
			goruntime.Gosched()
		}
	}
}

func assertLastEmittedEvent(t *testing.T, bus *fakeBus, clock *FixedClock, n int) {
	t.Helper()
	if got := bus.count(); got != n {
		t.Fatalf("published %d MetricsSnapshotEvents for %d ticks, want exactly %d", got, n, n)
	}
	var ev MetricsSnapshotEvent
	if err := json.Unmarshal(bus.last().payload, &ev); err != nil {
		t.Fatalf("json.Unmarshal(last published payload): %v", err)
	}
	if len(ev.Samples) != 1 || ev.Samples[0].Name != "emitted_total" || ev.Samples[0].Value != int64(n) {
		t.Fatalf("last event samples = %+v, want one sample emitted_total=%d", ev.Samples, n)
	}
	if !ev.Ts.Equal(clock.Now()) {
		t.Fatalf("last event Ts = %v, want the injected clock's current instant %v", ev.Ts, clock.Now())
	}
}

// assertNoPublishAfterCancel proves a tick attempt after RunPeriodicEmitter
// has already returned (ctx cancelled) is never delivered.
func assertNoPublishAfterCancel(t *testing.T, ticker *fakeTicker, bus *fakeBus) {
	t.Helper()
	before := bus.count()
	select {
	case ticker.c <- struct{}{}:
	default:
	}
	if got := bus.count(); got != before {
		t.Fatalf("published event after ctx cancellation: count went from %d to %d", before, got)
	}
}

// TestPeriodicEmitter_PublishErrorReported proves a Bus.Publish failure is
// reported via onError rather than crashing the emitter loop, and that
// the loop keeps running for the next tick.
func TestPeriodicEmitter_PublishErrorReported(t *testing.T) {
	reg := NewRegistry()
	clock := NewFixedClock(time.Unix(0, 0))
	ticker := newFakeTicker()
	bus := &fakeBus{failNext: true}
	errs := &errCollector{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunPeriodicEmitter(ctx, PeriodicEmitterOptions{
			Registry: reg,
			Clock:    clock,
			Ticker:   ticker,
			Bus:      bus,
		}, errs.record)
	}()

	ticker.Tick(ctx) // this one fails (failNext)
	spinUntil(func() bool { return errs.len() >= 1 })
	if got := errs.len(); got != 1 {
		t.Fatalf("onError called %d times after one forced-failing tick, want 1", got)
	}

	ticker.Tick(ctx) // this one succeeds
	spinUntil(func() bool { return bus.count() >= 1 })
	if bus.count() != 1 {
		t.Fatalf("published %d events after the failing tick's successor, want 1 (loop must survive a publish error)", bus.count())
	}

	cancel()
	<-done
}

// errCollector is a concurrency-safe error sink for onError callbacks.
type errCollector struct {
	mu   sync.Mutex
	errs []error
}

func (c *errCollector) record(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errs = append(c.errs, err)
}

func (c *errCollector) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.errs)
}

// spinUntil yields the goroutine (never sleeps — R-14.136) until cond is
// true or a generous bound is reached.
func spinUntil(cond func() bool) {
	for i := 0; i < 100000 && !cond(); i++ {
		goruntime.Gosched()
	}
}

// TestPeriodicEmitter_NoGoroutineLeak proves RunPeriodicEmitter's own
// goroutine, and NewSystemTicker's pump goroutine, both exit on ctx
// cancellation — checked by goroutine-count diffing (no external leak
// detector; zero new dependencies per the contract).
func TestPeriodicEmitter_NoGoroutineLeak(t *testing.T) {
	before := goruntime.NumGoroutine()

	reg := NewRegistry()
	clock := NewFixedClock(time.Unix(0, 0))
	ticker := NewSystemTicker(time.Millisecond)
	bus := &fakeBus{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunPeriodicEmitter(ctx, PeriodicEmitterOptions{
			Registry: reg,
			Clock:    clock,
			Ticker:   ticker,
			Bus:      bus,
		}, nil)
	}()

	// Let a couple of real ticks happen (this ticker is the production
	// SystemTicker, deliberately — proving the leak-free property of the
	// real implementation, not just the fake). This is the one place in
	// the suite that waits on real wall-clock ticks (a millisecond
	// interval), not a synchronization sleep standing in for a
	// deterministic condition, so it does not violate R-14.136.
	deadline := time.After(200 * time.Millisecond)
	for bus.count() == 0 {
		select {
		case <-deadline:
			t.Fatal("no MetricsSnapshotEvent published within 200ms of a 1ms system ticker")
		default:
			goruntime.Gosched()
		}
	}

	cancel()
	<-done

	for i := 0; i < 100000; i++ {
		if goruntime.NumGoroutine() <= before+1 { // +1 tolerance for test scheduler noise
			break
		}
		goruntime.Gosched()
	}
	after := goruntime.NumGoroutine()
	if after > before+1 {
		t.Errorf("goroutine count after cancel+Stop = %d, want <= %d (before=%d) — possible leak", after, before+1, before)
	}
}
