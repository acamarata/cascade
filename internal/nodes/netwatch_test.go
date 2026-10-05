package nodes

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
)

func fakeInterfaces(names ...string) InterfacesFunc {
	return func() ([]IfaceInfo, error) {
		out := make([]IfaceInfo, 0, len(names))
		for _, n := range names {
			out = append(out, IfaceInfo{Name: n})
		}
		return out, nil
	}
}

// syncTicker is an unbuffered Ticker: fire returns only once the watcher's
// Run loop has received the tick, which it does only after the previous
// pollOnce has fully returned. That makes "the previous poll is finished" an
// observable fact instead of something a sleep has to guess.
type syncTicker struct{ ch chan struct{} }

func (s *syncTicker) C() <-chan struct{} { return s.ch }
func (s *syncTicker) Stop()              {}

// fire hands one tick to the watcher, failing with a clear message if the
// Run loop does not take it within the bound.
func (s *syncTicker) fire(t *testing.T) {
	t.Helper()
	select {
	case s.ch <- struct{}{}:
	case <-time.After(netwatchWait):
		t.Fatalf("watcher did not receive the tick within %v", netwatchWait)
	}
}

// netwatchWait bounds every wait on the watcher. It is a failure bound, not a
// synchronisation delay: a healthy run never waits on it.
const netwatchWait = 10 * time.Second

// expectStep waits for the next recorded watcher step and fails unless it is
// want. The steps arrive in the order the watcher performs them, so a route
// change that lands before the first snapshot shows up as a wrong step here
// rather than as a flaky event count.
func expectStep(t *testing.T, steps <-chan string, want string) {
	t.Helper()
	select {
	case got := <-steps:
		if got != want {
			t.Fatalf("watcher step = %q, want %q", got, want)
		}
	case <-time.After(netwatchWait):
		t.Fatalf("timed out after %v waiting for watcher step %q", netwatchWait, want)
	}
}

// netwatchRig wires a NetworkWatcher to injected fakes and records, in order,
// each default-route read (the last read of a poll's fingerprint) and each
// change callback (after the publish) on steps.
type netwatchRig struct {
	ticker    *syncTicker
	bus       *lockedBusPublisher
	route     atomic.Bool
	triggered atomic.Int64
	steps     chan string
	cancel    context.CancelFunc
	done      chan struct{}
}

func newNetwatchRig(t *testing.T) *netwatchRig {
	t.Helper()
	r := &netwatchRig{
		ticker: &syncTicker{ch: make(chan struct{})},
		bus:    &lockedBusPublisher{inner: &captureBus{}, mu: &sync.Mutex{}},
		steps:  make(chan string, 16),
		done:   make(chan struct{}),
	}
	w := NewNetworkWatcher(NetworkWatcherDeps{
		Ticker:     r.ticker,
		Interfaces: fakeInterfaces("en0"),
		DefaultRoute: func() bool {
			v := r.route.Load()
			r.steps <- fmt.Sprintf("route=%t", v)
			return v
		},
		Bus: r.bus,
		OnChange: func() {
			r.triggered.Add(1)
			r.steps <- "change"
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	t.Cleanup(cancel)
	go func() { w.Run(ctx); close(r.done) }()
	return r
}

func (r *netwatchRig) stop(t *testing.T) {
	t.Helper()
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(netwatchWait):
		t.Fatalf("watcher did not stop within %v of cancel", netwatchWait)
	}
}

func TestNetworkWatcherDetectsRouteChange(t *testing.T) {
	r := newNetwatchRig(t)

	// First poll: the first observation is always a change. The route is
	// still false here, so the baseline cannot absorb the later flip.
	r.ticker.fire(t)
	expectStep(t, r.steps, "route=false")
	expectStep(t, r.steps, "change")
	if n := r.bus.len(); n != 1 {
		t.Fatalf("first pass: published %d events, want exactly 1 (first observation is always a change)", n)
	}
	if got := r.triggered.Load(); got != 1 {
		t.Fatalf("triggered = %d, want 1", got)
	}

	// Same fingerprint: the poll reads the route and reports no change.
	r.ticker.fire(t)
	expectStep(t, r.steps, "route=false")

	// The unchanged poll has taken its fingerprint, so flipping the route
	// now cannot reach it. The next fire returns only after that poll has
	// finished, and a spurious publish from it would surface as a "change"
	// step ahead of the "route=true" read below.
	r.route.Store(true)
	r.ticker.fire(t)
	expectStep(t, r.steps, "route=true")
	expectStep(t, r.steps, "change")
	if n := r.bus.len(); n != 2 {
		t.Fatalf("route-change pass: published %d events, want 2 (the unchanged pass must publish nothing)", n)
	}
	if got := r.triggered.Load(); got != 2 {
		t.Fatalf("triggered = %d, want 2 after a real network change", got)
	}
	r.stop(t)
}

// lockedBusPublisher is an EventBus that serializes Publish calls behind
// mu so a concurrently-reading test goroutine sees a consistent count.
type lockedBusPublisher struct {
	inner *captureBus
	mu    *sync.Mutex
}

func (b *lockedBusPublisher) Publish(ctx context.Context, ns string, kind events.EventKind, source string, payload []byte) (events.Event, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.inner.Publish(ctx, ns, kind, source, payload)
}

func (b *lockedBusPublisher) len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.inner.published)
}

func TestFingerprintOrderIndependent(t *testing.T) {
	w1 := NewNetworkWatcher(NetworkWatcherDeps{Ticker: newFakeTicker(), Interfaces: fakeInterfaces("en0", "en1"), DefaultRoute: func() bool { return true }})
	w2 := NewNetworkWatcher(NetworkWatcherDeps{Ticker: newFakeTicker(), Interfaces: fakeInterfaces("en1", "en0"), DefaultRoute: func() bool { return true }})
	fp1, err := w1.fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	fp2, err := w2.fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if fp1 != fp2 {
		t.Fatalf("fingerprint depends on interface order: %q vs %q", fp1, fp2)
	}
}
