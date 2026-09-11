//go:build !windows

// Purpose: unit coverage for soulDivergenceSink.SoulDiverged: asserts it
//   publishes the event's own EventName() as the bus Kind, on the
//   "memory" namespace, from soulEventSource, and that a nil bus
//   discards rather than panics.
// SPORT: cmd/cascade (ADD, coverage-floor fix).

package main

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/memory"
)

func TestSoulDivergenceSink_SoulDiverged_PublishesEventName(t *testing.T) {
	bus := newMemoryJobSinkTestBus(t)
	sink := soulDivergenceSink{bus: bus}
	ev := memory.DivergenceEvent{Version: 3, LastReconciledVersion: 2, StoredHash: "aaa", FileHash: "bbb"}
	if err := sink.SoulDiverged(context.Background(), ev); err != nil {
		t.Fatalf("SoulDiverged: %v", err)
	}
	evs, err := bus.Replay(context.Background(), soulEventNamespace, 0)
	if err != nil {
		t.Fatalf("Replay(%s): %v", soulEventNamespace, err)
	}
	if len(evs) == 0 {
		t.Fatal("Replay: no events published")
	}
	last := evs[len(evs)-1]
	if string(last.Kind) != ev.EventName() {
		t.Errorf("published Kind = %q, want %q", last.Kind, ev.EventName())
	}
	if last.Source != soulEventSource {
		t.Errorf("published Source = %q, want %q", last.Source, soulEventSource)
	}
}

// TestSoulDivergenceSink_NilBus_Discards proves the documented no-bus
// degradation: a nil bus returns nil rather than panicking.
func TestSoulDivergenceSink_NilBus_Discards(t *testing.T) {
	sink := soulDivergenceSink{}
	if err := sink.SoulDiverged(context.Background(), memory.DivergenceEvent{}); err != nil {
		t.Fatalf("SoulDiverged with nil bus: %v", err)
	}
}
