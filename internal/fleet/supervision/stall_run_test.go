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
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
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

func TestRunSeedFailureIsTypedAndStopsPoll(t *testing.T) {
	fx := newStallFixture(t, fixtureOpts{})
	fx.sess.listErr = cascade.New(cascade.KindUnavailable, "db down")
	err := fx.d.Run(context.Background(), fx.bus)
	if ce, ok := err.(*cascade.Error); !ok || ce.Kind != cascade.KindUnavailable || ce.Msg != "stall detector could not seed from the session store" {
		t.Fatalf("Run = %v, want Unavailable seed failure", err)
	}
	if fx.d.Alive() {
		t.Error("detector is Alive after a failed seed")
	}
}

func TestIsExpectedEscalationMatchesSentinelsByIdentity(t *testing.T) {
	if !isExpectedEscalation(governor.EscalationExhausted) {
		t.Error("EscalationExhausted must be expected")
	}
	wrapped := cascade.Wrapf(cascade.KindUnavailable, governor.ErrEscalationRungFailed, "rung failed: %v", "x")
	if !isExpectedEscalation(wrapped) {
		t.Error("a wrapped ErrEscalationRungFailed must be expected")
	}
	// Same Kind as ErrEscalationRungFailed (Unavailable), different identity.
	if isExpectedEscalation(cascade.New(cascade.KindUnavailable, "journal down")) {
		t.Error("an unrelated KindUnavailable error was swallowed as an expected rung failure")
	}
	if isExpectedEscalation(errors.Join(governor.EscalationExhausted, cascade.New(cascade.KindUnavailable, "bus down"))) {
		t.Error("a joined error with an unexpected part was swallowed")
	}
	if !isExpectedEscalation(errors.Join(governor.EscalationExhausted, wrapped)) {
		t.Error("a joined error of only expected parts must be expected")
	}
	if isExpectedEscalation(nil) {
		t.Error("nil is not an escalation outcome")
	}
}

func TestClosedOrIdleSessionUnwatched(t *testing.T) {
	ctx := context.Background()
	fx := newStallFixture(t, fixtureOpts{})
	fx.markAlive()
	now := fx.clock.Now()
	for _, rec := range []struct{ id, state string }{{"c1", "closed"}, {"i1", "idle"}, {"a1", "active"}} {
		fx.sess.put(rec.id, rec.state, fx.nowMs())
		sess, _ := fx.sess.Get(ctx, rec.id)
		fx.d.handleSession(ctx, sessionEvent(t, sess, now))
	}
	if got := fx.d.tracker.Watched(); len(got) != 1 || got[0] != "a1" {
		t.Fatalf("watched = %v, want only [a1]", got)
	}
	fx.clock.Advance(5 * time.Minute)
	if err := fx.d.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if _, ok := fx.d.lookupStallEvent("a1"); !ok {
		t.Error("an active session that went quiet was not escalated")
	}
	for _, id := range []string{"c1", "i1"} {
		if _, ok := fx.d.lookupStallEvent(id); ok || fx.escalations(id) != 0 {
			t.Errorf("%s was escalated, want never", id)
		}
	}
	// A watched session that turns idle or closed is dropped at once.
	for _, state := range []string{"idle", "closed"} {
		fx.sess.put("a2", "active", fx.nowMs())
		rec, _ := fx.sess.Get(ctx, "a2")
		fx.d.handleSession(ctx, sessionEvent(t, rec, fx.clock.Now()))
		rec.State = state
		fx.d.handleSession(ctx, sessionEvent(t, rec, fx.clock.Now()))
		for _, id := range fx.d.tracker.Watched() {
			if id == "a2" {
				t.Errorf("a2 still watched after turning %s", state)
			}
		}
	}
}

func TestObserveIgnoresUnknownOrClosedSession(t *testing.T) {
	ctx := context.Background()
	fx := newStallFixture(t, fixtureOpts{})
	fx.sess.put("closed", "closed", fx.nowMs())
	fx.sess.put("live", "active", fx.nowMs())
	now := fx.nowMs()
	for _, id := range []string{"ghost", "closed"} {
		if err := fx.d.Observe(ctx, StallSignal{Kind: SignalBlocked, SessionID: id, At: now}); err != nil {
			t.Errorf("blocked for %s: %v, want nil (ignored)", id, err)
		}
		for i := 0; i < 3; i++ {
			if err := fx.d.Observe(ctx, StallSignal{Kind: SignalGateDenied, SessionID: id, JobID: "j-" + id, At: now}); err != nil {
				t.Errorf("gate-denied for %s: %v, want nil (ignored)", id, err)
			}
		}
		if _, ok := fx.d.lookupStallEvent(id); ok || fx.escalations(id) != 0 || len(fx.stalledEvents()) != 0 {
			t.Errorf("signals for %s filed something, want nothing", id)
		}
	}
	// Controls: the same signals for a live session do file.
	if err := fx.d.Observe(ctx, StallSignal{Kind: SignalBlocked, SessionID: "live", At: now}); err != nil {
		t.Fatalf("blocked for live: %v", err)
	}
	if ev, ok := fx.d.lookupStallEvent("live"); !ok || ev.StallKind != StallKindBlocked {
		t.Errorf("blocked for a live session = (%+v, %v), want a blocked event", ev, ok)
	}
	for i := 0; i < 3; i++ {
		_ = fx.d.Observe(ctx, StallSignal{Kind: SignalGateDenied, SessionID: "live", JobID: "j-live", At: now})
	}
	if ev, _ := fx.d.lookupStallEvent("live"); ev.StallKind != StallKindGateDenied {
		t.Errorf("three denials for a live session left %+v, want gate-denied", ev)
	}
	// A lookup failure other than not-found is a typed error, never "watched".
	fx.sess.getErr = cascade.New(cascade.KindUnavailable, "db down")
	err := fx.d.Observe(ctx, StallSignal{Kind: SignalBlocked, SessionID: "live", At: now})
	if ce, ok := err.(*cascade.Error); !ok || ce.Kind != cascade.KindUnavailable || ce.Msg != "stall detector session lookup failed" {
		t.Errorf("lookup failure = %v, want Unavailable \"stall detector session lookup failed\"", err)
	}
}
