//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestNotifyDropsRegistered: the production composition registers
// notify.stats, installs the drop sink on its one Service and runs the
// notify.dropped publisher on the manifest.
func TestNotifyDropsRegistered(t *testing.T) {
	built := countNotifyServices(t)
	t.Setenv("HOME", t.TempDir())
	deps := newRunTestDeps(t, nonexistentExecutable(t))
	var reg *rpc.Registry
	opts, cleanups, err := composeDaemon(context.Background(), deps, func(r *rpc.Registry, _ *rpc.SSEMux) { reg = r })
	t.Cleanup(func() { runCleanupsLIFO(cleanups) })
	if err != nil {
		t.Fatalf("composeDaemon: %v", err)
	}
	if reg == nil || !reg.Registered(notify.StatsMethod) {
		t.Fatalf("%s not registered on the daemon registry", notify.StatsMethod)
	}
	svcs := built()
	if len(svcs) != 1 {
		t.Fatalf("built %d notify.Service values, want 1", len(svcs))
	}
	err = svcs[0].SetDropSink(func(context.Context, notify.DropEvent) {})
	if k, _ := cascade.KindOf(err); k != cascade.KindConflict {
		t.Fatalf("SetDropSink after composition = %v, want KindConflict (sink installed)", err)
	}
	if _, ok := subsystemState(opts.Manifest, notifyDropsSubsystem); !ok {
		t.Fatalf("subsystem %q not on the manifest", notifyDropsSubsystem)
	}
}

// fakeDropBus records every Publish.
type fakeDropBus struct {
	mu  sync.Mutex
	got []events.Event
	ns  []string
}

func (b *fakeDropBus) Publish(_ context.Context, ns string, kind events.EventKind, src string, p []byte) (events.Event, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ns = append(b.ns, ns)
	b.got = append(b.got, events.Event{Kind: kind, Source: src, Payload: p})
	return events.Event{}, nil
}

func (b *fakeDropBus) published() ([]string, []events.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.ns...), append([]events.Event(nil), b.got...)
}

// TestNotifyDroppedPublished: a drop reaches the bus as notify.dropped on
// the notify namespace with exactly {id, source, priority, reason}, and a
// full sink queue never blocks the producer.
func TestNotifyDroppedPublished(t *testing.T) {
	queue := make(chan notify.DropEvent, 1)
	sink := queueDropSink(queue)
	sink(context.Background(), notify.DropEvent{ID: "d1", Source: "src", Reason: notify.DropQueueFull, Priority: notify.PriorityHigh})
	done := make(chan struct{})
	go func() { sink(context.Background(), notify.DropEvent{ID: "d2"}); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("drop sink blocked on a full queue")
	}
	bus := &fakeDropBus{}
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan error, 1)
	go func() { ended <- publishDrops(ctx, bus, queue) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, evs := bus.published(); len(evs) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("notify.dropped never published")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-ended; err != nil {
		t.Fatalf("publishDrops: %v", err)
	}
	ns, evs := bus.published()
	if ns[0] != daemon.NotifyNamespace || evs[0].Kind != notifyDroppedKind {
		t.Fatalf("published %s/%s, want %s/%s", ns[0], evs[0].Kind, daemon.NotifyNamespace, notifyDroppedKind)
	}
	var body map[string]string
	if err := json.Unmarshal(evs[0].Payload, &body); err != nil {
		t.Fatalf("payload: %v", err)
	}
	want := map[string]string{"id": "d1", "source": "src", "priority": notify.PriorityHigh.String(), "reason": notify.DropQueueFull}
	if len(body) != len(want) {
		t.Fatalf("payload %v, want exactly %v", body, want)
	}
	for k, v := range want {
		if body[k] != v {
			t.Fatalf("payload[%s] = %q, want %q", k, body[k], v)
		}
	}
}
