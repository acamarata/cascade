package supervision

// Run/Poll/RunPoll behaviour of the composed detector: bounded polling,
// the session watch rule, head-start subscriptions, event-time stamping
// and fail-closed handling of a dead subscription.

import (
	"context"
	"encoding/json"
	goruntime "runtime"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// startRun runs d.Run against bus and returns a stop func that cancels and
// waits for Run's result.
func startRun(t *testing.T, d *Detector, bus StallBus) (stop func() error, result <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, bus) }()
	t.Cleanup(cancel)
	eventually(t, func() bool {
		select {
		case err := <-done:
			t.Fatalf("Run returned early: %v", err)
		default:
		}
		return d.Alive()
	})
	return func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return after cancel")
			return nil
		}
	}, done
}

func publishDenials(t *testing.T, bus *events.Bus, n int, jobID, sessionID string) {
	t.Helper()
	for i := 0; i < n; i++ {
		payload, err := json.Marshal(gateDeniedPayload{JobID: jobID, SessionID: sessionID, TicketID: "t", Reason: "r", Attempt: i + 1})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := bus.Publish(context.Background(), gateDeniedNamespace, gateDeniedKind, "gate", payload); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}
}

func TestDetectorPollAdvancesOncePerRungDelay(t *testing.T) {
	ctx := context.Background()
	fx := newStallFixture(t, fixtureOpts{})
	fx.markAlive()
	stallQuiet(fx, "s1")
	for i := 0; i < 10; i++ {
		_ = fx.d.Poll(ctx)
	}
	if got := fx.escalations("s1"); got != 1 {
		t.Fatalf("10 Polls inside RungDelay advanced %d times, want 1", got)
	}
	fx.clock.Advance(testRungDelay - time.Second)
	_ = fx.d.Poll(ctx)
	if got := fx.escalations("s1"); got != 1 {
		t.Fatalf("a Poll 1s short of RungDelay advanced (total %d), want still 1", got)
	}
	fx.clock.Advance(time.Second)
	_ = fx.d.Poll(ctx)
	if got := fx.escalations("s1"); got != 2 {
		t.Fatalf("a Poll at +RungDelay left %d advances, want 2", got)
	}
}

func TestRunPollStopsOnContextCancel(t *testing.T) {
	fx := newStallFixture(t, fixtureOpts{})
	fx.markAlive()
	before := goruntime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	ticker := newFakePollTicker()
	done := make(chan struct{})
	go func() { fx.d.RunPoll(ctx, ticker); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunPoll did not return within 1s of cancel")
	}
	eventually(t, func() bool { return goruntime.NumGoroutine() <= before })
}

func TestRunPollTicksOnRuntimeClock(t *testing.T) {
	fx := newStallFixture(t, fixtureOpts{})
	fx.markAlive()
	stallQuiet(fx, "s1")
	// pollRound runs RunPoll for the given number of ticks and waits for it
	// to stop, so the frozen clock is only advanced while no goroutine reads
	// it (FixedClock is not safe for concurrent Advance).
	pollRound := func(ticks int) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		ticker := newFakePollTicker()
		done := make(chan struct{})
		go func() { fx.d.RunPoll(ctx, ticker); close(done) }()
		// The extra tick is only accepted once the loop is back at its
		// select, i.e. every earlier Poll has finished; being inside
		// RungDelay it never advances the ladder itself.
		for i := 0; i < ticks+1; i++ {
			ticker.tick()
		}
		eventually(t, func() bool { return len(ticker.c) == 0 })
		cancel()
		<-done
	}
	pollRound(1)
	if got := fx.escalations("s1"); got != 1 {
		t.Fatalf("first tick left %d advances, want 1", got)
	}
	pollRound(3) // same frozen instant: RungDelay has not elapsed
	if got := fx.escalations("s1"); got != 1 {
		t.Fatalf("ticks inside RungDelay advanced the ladder to %d, want 1", got)
	}
	fx.clock.Advance(testRungDelay)
	pollRound(1)
	if got := fx.escalations("s1"); got != 2 {
		t.Fatalf("tick at +RungDelay left %d advances, want 2", got)
	}
}

func TestDetectorSubscribesFromHead(t *testing.T) {
	ctx := context.Background()
	fx := newStallFixture(t, fixtureOpts{})
	fx.sess.put("old", "active", fx.nowMs())
	fx.sess.put("new", "active", fx.nowMs())
	publishDenials(t, fx.bus, 50, "job-old", "old") // fresh timestamps: only head-start keeps them out
	if hist, err := fx.bus.Replay(ctx, gateDeniedNamespace, 0); err != nil || len(hist) != 50 {
		t.Fatalf("seeded history = (%d, %v), want 50 events", len(hist), err)
	}
	stop, _ := startRun(t, fx.d, fx.bus)
	publishDenials(t, fx.bus, 3, "job-new", "new")
	eventually(t, func() bool {
		ev, ok := fx.d.lookupStallEvent("new")
		return ok && ev.StallKind == StallKindGateDenied
	})
	if ev, ok := fx.d.lookupStallEvent("old"); ok {
		t.Errorf("50 historic denials triggered %+v, want nothing", ev)
	}
	if err := stop(); err != nil {
		t.Errorf("Run after cancel = %v, want nil", err)
	}
}

func TestReplayedGateDenialsUseEventTime(t *testing.T) {
	ctx := context.Background()
	busClock := runtime.NewFixedClock(stallT0.Add(-2 * time.Hour))
	fx := newStallFixture(t, fixtureOpts{})
	fx.bus = events.New(fx.kvBus, busClock) // events carry the bus clock's time
	fx.sess.put("old", "active", fx.nowMs())
	fx.sess.put("new", "active", fx.nowMs())

	// First start commits the cursors at the (empty) head, then stops.
	stop, _ := startRun(t, fx.d, fx.bus)
	if err := stop(); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	// Three denials 2h old land while the daemon is down.
	publishDenials(t, fx.bus, 3, "job-old", "old")
	fx.d = rebuildDetector(t, fx)
	stop, _ = startRun(t, fx.d, fx.bus)
	// Three fresh ones arrive after the restart.
	busClock.Advance(2 * time.Hour)
	publishDenials(t, fx.bus, 3, "job-new", "new")
	eventually(t, func() bool {
		ev, ok := fx.d.lookupStallEvent("new")
		return ok && ev.StallKind == StallKindGateDenied
	})
	if ev, ok := fx.d.lookupStallEvent("old"); ok || fx.escalations("old") != 0 {
		t.Errorf("three 2h-old denials escalated (%+v), want none: replays must keep their age", ev)
	}
	if err := stop(); err != nil {
		t.Errorf("Run after cancel = %v, want nil", err)
	}
	_ = ctx
}

// rebuildDetector builds a fresh detector over fx's stores, as a restart.
func rebuildDetector(t *testing.T, fx *stallFixture) *Detector {
	t.Helper()
	policy := onePerRung()
	policy.RungDelay = testRungDelay
	rungs := RungConfig{Sessions: fx.sess, Directives: fx.dirs, HydrationEnabled: true, BudgetTokens: testBudget}
	d, err := NewStallDetector(fx.jr, rungs, fx.attn, fx.req, policy, time.Minute, fx.clock, NewBusStallPublisher(fx.bus))
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	return d
}

func TestDetectorSeedsLiveSessionsAtStart(t *testing.T) {
	ctx := context.Background()
	fx := newStallFixture(t, fixtureOpts{})
	quiet := fx.nowMs() - (10 * time.Minute).Milliseconds()
	fx.sess.put("a-quiet", "active", quiet)
	fx.sess.put("a-fresh", "active", fx.nowMs())
	fx.sess.put("closed", "closed", quiet)
	fx.sess.put("idle", "idle", quiet)
	startRun(t, fx.d, fx.bus)
	if err := fx.d.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if ev, ok := fx.d.lookupStallEvent("a-quiet"); !ok || ev.StallKind != StallKindIdle || ev.StalledSince != quiet {
		t.Errorf("a-quiet event = (%+v, %v), want idle since its UpdatedAt", ev, ok)
	}
	if left, _ := fx.dirs.Drain(ctx, "a-quiet"); len(left) != 1 || left[0].Kind != DirectiveRetry {
		t.Errorf("a-quiet directives = %v, want the retry rung's one directive", left)
	}
	for _, id := range []string{"a-fresh", "closed", "idle"} {
		if _, ok := fx.d.lookupStallEvent(id); ok || fx.escalations(id) != 0 {
			t.Errorf("%s was escalated, want not (fresh active, closed and idle are not stalled)", id)
		}
	}
}

// TestSeedWithoutActivityTimeStartsAtNow: a stored record with UpdatedAt 0
// carries no activity time, so it seeds at the injected clock's now (as
// handleSession does), not the epoch. It is not escalated at the first
// Poll, and it is once a full threshold passes.
func TestSeedWithoutActivityTimeStartsAtNow(t *testing.T) {
	ctx := context.Background()
	fx := newStallFixture(t, fixtureOpts{})
	seededAt := fx.nowMs()
	fx.sess.put("a0", "active", 0)
	startRun(t, fx.d, fx.bus)
	if err := fx.d.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if ev, ok := fx.d.lookupStallEvent("a0"); ok || fx.escalations("a0") != 0 {
		t.Fatalf("a0 escalated at the first Poll (%+v), want a fresh session left alone", ev)
	}
	fx.clock.Advance(2 * time.Minute)
	if err := fx.d.Poll(ctx); err != nil {
		t.Fatalf("control Poll: %v", err)
	}
	if ev, ok := fx.d.lookupStallEvent("a0"); !ok || ev.StallKind != StallKindIdle || ev.StalledSince != seededAt {
		t.Errorf("control event = (%+v, %v), want idle since the seed instant %d", ev, ok, seededAt)
	}
}

func TestSubscriptionLossStopsPoll(t *testing.T) {
	ctx := context.Background()
	fx := newStallFixture(t, fixtureOpts{})
	fx.sess.put("a1", "active", fx.nowMs()-(10*time.Minute).Milliseconds())
	_, result := startRun(t, fx.d, fx.bus)
	if err := fx.bus.Unsubscribe(sessionsChangedNamespace, stallCursorName); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}
	var err error
	select {
	case err = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its subscription died")
	}
	ce, ok := err.(*cascade.Error)
	if !ok || ce.Kind != cascade.KindUnavailable || ce.Msg != "stall detector subscription lost" {
		t.Fatalf("Run returned %v, want Unavailable \"stall detector subscription lost\"", err)
	}
	if fx.d.Alive() {
		t.Error("detector still reports Alive after losing its subscription")
	}
	if err := fx.d.Poll(ctx); err != errPollNotAlive {
		t.Fatalf("Poll after loss = %v, want errPollNotAlive by identity", err)
	}
	if _, ok := fx.d.lookupStallEvent("a1"); ok || fx.escalations("a1") != 0 {
		t.Error("Poll advanced a session after the subscription died; stored ladder state must be unchanged")
	}
	// Control: a live detector would have escalated this quiet session, as
	// "unknown" (the mass-escalation this guard exists to stop).
	fx.markAlive()
	_ = fx.d.Poll(ctx)
	if ev, ok := fx.d.lookupStallEvent("a1"); !ok || ev.StallKind != StallKindUnknown || fx.escalations("a1") != 1 {
		t.Errorf("control Poll = (%+v, %v), want an unknown-kind advance", ev, ok)
	}
}
