package nodes

import (
	"context"
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

func TestNetworkWatcherDetectsRouteChange(t *testing.T) {
	ticker := newFakeTicker()
	inner := &captureBus{}
	var mu sync.Mutex
	bus := &lockedBusPublisher{inner: inner, mu: &mu}
	var triggered atomic.Int64
	var route atomic.Bool
	w := NewNetworkWatcher(NetworkWatcherDeps{
		Ticker:       ticker,
		Interfaces:   fakeInterfaces("en0"),
		DefaultRoute: route.Load,
		Bus:          bus,
		OnChange:     func() { triggered.Add(1) },
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	ticker.fire()
	time.Sleep(20 * time.Millisecond)
	if n := bus.len(); n != 1 {
		t.Fatalf("first pass: published %d events, want exactly 1 (first observation is always a change)", n)
	}
	if got := triggered.Load(); got != 1 {
		t.Fatalf("triggered = %d, want 1", got)
	}

	// Same fingerprint: no change, no new publish.
	ticker.fire()
	time.Sleep(20 * time.Millisecond)
	if n := bus.len(); n != 1 {
		t.Fatalf("unchanged pass: published %d events, want still 1", n)
	}

	// Flip the default route: a real change.
	route.Store(true)
	ticker.fire()
	time.Sleep(20 * time.Millisecond)
	if n := bus.len(); n != 2 {
		t.Fatalf("route-change pass: published %d events, want 2", n)
	}
	if got := triggered.Load(); got != 2 {
		t.Fatalf("triggered = %d, want 2 after a real network change", got)
	}

	cancel()
	<-done
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
