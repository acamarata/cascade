//go:build !windows

// Purpose: unit coverage for memoryJobEventSink's five untested publish
//   adapters (CandidatePromoted, CandidateReverted, ReviewActed,
//   MemoryWeeklyDigestReady, MemoryStaleQueued): each is asserted to
//   publish the event's own EventName() as the bus Kind, on the
//   "memory" namespace, from memoryJobsEventSource - not merely that the
//   call does not panic.
// SPORT: cmd/cascade (ADD, coverage-floor fix).

package main

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/memory/review"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

func newMemoryJobSinkTestBus(t *testing.T) *events.Bus {
	t.Helper()
	clock := runtime.NewFixedClock(time.Unix(1_700_000_000, 0))
	bus := events.New(storetest.NewMemStore(), clock)
	t.Cleanup(func() { _ = bus.Close() })
	return bus
}

// assertLastMemoryEvent replays the "memory" namespace and asserts the
// last published event's Kind and Source match wantKind and
// memoryJobsEventSource.
func assertLastMemoryEvent(t *testing.T, bus *events.Bus, wantKind string) {
	t.Helper()
	evs, err := bus.Replay(context.Background(), memoryJobsEventNamespace, 0)
	if err != nil {
		t.Fatalf("Replay(%s): %v", memoryJobsEventNamespace, err)
	}
	if len(evs) == 0 {
		t.Fatal("Replay: no events published")
	}
	last := evs[len(evs)-1]
	if string(last.Kind) != wantKind {
		t.Errorf("published Kind = %q, want %q", last.Kind, wantKind)
	}
	if last.Source != memoryJobsEventSource {
		t.Errorf("published Source = %q, want %q", last.Source, memoryJobsEventSource)
	}
	if len(last.Payload) == 0 {
		t.Error("published Payload is empty, want the marshaled event body")
	}
}

func TestMemoryJobEventSink_CandidatePromoted_PublishesEventName(t *testing.T) {
	bus := newMemoryJobSinkTestBus(t)
	sink := memoryJobEventSink{bus: bus}
	ev := memory.PromotionEvent{Name: "n1", Kind: memory.KindUser, RefCount: 2}
	if err := sink.CandidatePromoted(context.Background(), ev); err != nil {
		t.Fatalf("CandidatePromoted: %v", err)
	}
	assertLastMemoryEvent(t, bus, ev.EventName())
}

func TestMemoryJobEventSink_CandidateReverted_PublishesEventName(t *testing.T) {
	bus := newMemoryJobSinkTestBus(t)
	sink := memoryJobEventSink{bus: bus}
	ev := memory.RevertEvent{Name: "n1", Kind: memory.KindUser, Reason: "stale"}
	if err := sink.CandidateReverted(context.Background(), ev); err != nil {
		t.Fatalf("CandidateReverted: %v", err)
	}
	assertLastMemoryEvent(t, bus, ev.EventName())
}

func TestMemoryJobEventSink_ReviewActed_PublishesEventName(t *testing.T) {
	bus := newMemoryJobSinkTestBus(t)
	sink := memoryJobEventSink{bus: bus}
	ev := review.ActionEvent{ID: "addr-1", Kind: memory.KindUser, Action: "approve", Changed: true}
	if err := sink.ReviewActed(context.Background(), ev); err != nil {
		t.Fatalf("ReviewActed: %v", err)
	}
	assertLastMemoryEvent(t, bus, ev.EventName())
}

func TestMemoryJobEventSink_MemoryWeeklyDigestReady_PublishesEventName(t *testing.T) {
	bus := newMemoryJobSinkTestBus(t)
	sink := memoryJobEventSink{bus: bus}
	ev := memory.MemoryWeeklyDigest{MinRefCount: 3}
	if err := sink.MemoryWeeklyDigestReady(context.Background(), ev); err != nil {
		t.Fatalf("MemoryWeeklyDigestReady: %v", err)
	}
	assertLastMemoryEvent(t, bus, ev.EventName())
}

func TestMemoryJobEventSink_MemoryStaleQueued_PublishesEventName(t *testing.T) {
	bus := newMemoryJobSinkTestBus(t)
	sink := memoryJobEventSink{bus: bus}
	ev := memory.StaleQueuedEvent{StaleIDs: []string{"addr-1"}}
	if err := sink.MemoryStaleQueued(context.Background(), ev); err != nil {
		t.Fatalf("MemoryStaleQueued: %v", err)
	}
	assertLastMemoryEvent(t, bus, ev.EventName())
}

// TestMemoryJobEventSink_NilBus_Discards proves the documented no-bus
// degradation: a nil bus returns nil rather than panicking.
func TestMemoryJobEventSink_NilBus_Discards(t *testing.T) {
	sink := memoryJobEventSink{}
	ev := memory.StaleQueuedEvent{StaleIDs: []string{"addr-1"}}
	if err := sink.MemoryStaleQueued(context.Background(), ev); err != nil {
		t.Fatalf("MemoryStaleQueued with nil bus: %v", err)
	}
}
