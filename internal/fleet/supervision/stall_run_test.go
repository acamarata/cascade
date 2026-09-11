package supervision

// This file is NOT in P1-E18-W4-S39-T5's files_scope, which lists only
// stall_test.go for stall.go's coverage. It exists per R-16.79: moving
// Detector.Run/RunPoll's bus- and ticker-driven tests out of stall_test.go
// was required to keep that file under Art.10.3's 300-line cap (it had
// reached 393 lines with them inline) — a cap-driven split, not a scope
// expansion of what is tested, joins the ticket's authorized write set
// automatically (the identical precedent P1-E13-W3-S27-T1's journal
// records for its own store.go/entry.go split).

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// TestDetectorRunTouchesAndObservesFromBus drives Detector.Run against
// a real *events.Bus: a blocked-state session event both records
// progress and produces a StallSignal, all within one delivery cycle.
// Every channel receive below (via waitUntil) is bounded, never an
// unguarded <-ch.
func TestDetectorRunTouchesAndObservesFromBus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bus := events.New(storetest.NewMemStore(), runtime.NewFixedClock(time.Now()))
	d, _, _ := newTestDetector(t, time.Now(), fakeRetryer{}, fakeEnricher{}, &fakeApprovalRequester{})

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, bus) }()
	waitUntil(t, 2*time.Second, d.Alive)

	rec := sessions.SessionRecord{SessionID: "sess-bus", State: "blocked"}
	payload, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := bus.Publish(ctx, "fleet.sessions", events.EventKind("fleet.sessions.changed"), "sessions:sess-bus", payload); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	waitUntil(t, 2*time.Second, func() bool {
		_, ok := d.lookupStallEvent("sess-bus")
		return ok
	})
	ev, ok := d.lookupStallEvent("sess-bus")
	if !ok || ev.StallKind != StallKindBlocked {
		t.Fatalf("lookupStallEvent(sess-bus) = (%v, %v), want (blocked, true)", ev, ok)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v after cancel, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// TestDetectorHandleGateFiresAfterThreeDenials drives the jobs.gate.denied
// bus subscription (Run's second stream) through three real Publish
// calls and proves the 3-in-30-minute rule fires end to end.
func TestDetectorHandleGateFiresAfterThreeDenials(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bus := events.New(storetest.NewMemStore(), runtime.NewFixedClock(time.Now()))
	d, _, _ := newTestDetector(t, time.Now(), fakeRetryer{}, fakeEnricher{}, &fakeApprovalRequester{})

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, bus) }()
	waitUntil(t, 2*time.Second, d.Alive)

	for i := 0; i < 3; i++ {
		payload, err := json.Marshal(gateDeniedPayload{JobID: "job-y", SessionID: "sess-gate-bus", TicketID: "t", Reason: "r", Attempt: i + 1})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := bus.Publish(ctx, gateDeniedNamespace, gateDeniedKind, "gate", payload); err != nil {
			t.Fatalf("Publish #%d: %v", i+1, err)
		}
	}

	waitUntil(t, 2*time.Second, func() bool {
		ev, ok := d.lookupStallEvent("sess-gate-bus")
		return ok && ev.StallKind == StallKindGateDenied
	})

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// fakePollTicker is a runtime.Ticker a test fires on demand, matching
// sampler_test.go's own fakeTicker convention (never a real time.Ticker
// in a test, R-14.136).
type fakePollTicker struct {
	c chan struct{}
}

func newFakePollTicker() *fakePollTicker     { return &fakePollTicker{c: make(chan struct{}, 1)} }
func (f *fakePollTicker) C() <-chan struct{} { return f.c }
func (f *fakePollTicker) Stop()              {}
func (f *fakePollTicker) tick()              { f.c <- struct{}{} }

// TestDetectorRunPollDrivesPollOnEachTick proves RunPoll actually calls
// Poll on every tick (a stale session becomes a recorded StallEvent)
// and returns promptly once ctx is canceled.
func TestDetectorRunPollDrivesPollOnEachTick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	d, _, clock := newTestDetector(t, time.Now(), fakeRetryer{}, fakeEnricher{}, &fakeApprovalRequester{})
	d.Touch("sess-ticked")
	clock.Advance(2 * time.Minute)

	ticker := newFakePollTicker()
	done := make(chan struct{})
	go func() { d.RunPoll(ctx, ticker); close(done) }()

	ticker.tick()
	waitUntil(t, 2*time.Second, func() bool {
		ev, ok := d.lookupStallEvent("sess-ticked")
		return ok && ev.StallKind == StallKindIdle
	})

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunPoll did not return after cancel")
	}
}
