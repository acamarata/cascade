package supervision

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeRetryer/fakeEnricher are Art.1-exempt test-only stub seams
// (governor.Retryer/ContextEnricher), matching escalation_test.go's own
// convention of stubbing the ladder's five seams in _test.go files only.
type fakeRetryer struct{ err error }

func (f fakeRetryer) Retry(context.Context, string) error { return f.err }

type fakeEnricher struct{ err error }

func (f fakeEnricher) Enrich(context.Context, string) (int, error) { return 0, f.err }

// onePerRung is a policy that forces the ladder to advance past a rung
// after exactly one Advance call at it, regardless of that call's own
// outcome (R-21.216) — the fastest way to drive Retry -> Context ->
// SupervisorTask -> Human across a small, fixed number of test calls.
func onePerRung() governor.EscalationPolicy {
	return governor.EscalationPolicy{
		MaxAttempts: map[governor.EscalationRung]int{
			governor.RungRetry: 1, governor.RungContext: 1,
			governor.RungSupervisorTask: 1, governor.RungHuman: 1,
		},
		ConfidenceThreshold: 0.5,
	}
}

func newTestDetector(t *testing.T, now time.Time, retryer governor.Retryer, enricher governor.ContextEnricher, req ApprovalRequester) (*Detector, *Store, *runtime.FixedClock) {
	t.Helper()
	clock := runtime.NewFixedClock(now)
	j := journal.New(storetest.NewMemStore(), clock, "stall-test")
	store := newTestStore(t, now)
	d := NewDetector(j, retryer, enricher, store, req, onePerRung(), time.Minute, clock)
	return d, store, clock
}

// TestDetectorEscalatesRetryContextSupervisorHuman drives a session
// through the full four-rung ladder with a failing Retry/Context seam,
// proving this ticket's own SupervisorCreator/HumanNotifier seams are
// the ones actually invoked at rungs 3 and 4: a real AttentionItem lands
// in the S-39.T1 Store, and a real approval request reaches the fake
// I/S-18.T3 seam, in the correct order.
func TestDetectorEscalatesRetryContextSupervisorHuman(t *testing.T) {
	ctx := context.Background()
	req := &fakeApprovalRequester{}
	d, store, _ := newTestDetector(t, time.Now(), fakeRetryer{err: cascade.New(cascade.KindUnavailable, "retry failed")}, fakeEnricher{err: cascade.New(cascade.KindUnavailable, "enrich failed")}, req)

	sig := StallSignal{Kind: SignalBlocked, SessionID: "sess-1", At: 1}

	// Call 1: Retry fails -> advances to Context.
	if err := d.Observe(ctx, sig); err == nil {
		t.Fatal("call 1: want a rung-failed error, got nil")
	}
	if items, _ := store.ListInScopes(ctx, []ScopeRef{sessionScope("sess-1")}, Filter{}); len(items) != 0 {
		t.Fatalf("call 1: attention queue already has %d items, want 0 (still at Context)", len(items))
	}

	// Call 2: Context fails -> advances to SupervisorTask.
	if err := d.Observe(ctx, sig); err == nil {
		t.Fatal("call 2: want a rung-failed error, got nil")
	}

	// Call 3: SupervisorTask's seam (this ticket's own) succeeds ->
	// pushes a real AttentionItem, Advance returns nil.
	if err := d.Observe(ctx, sig); err != nil {
		t.Fatalf("call 3: %v, want nil (supervisor-task seam succeeds)", err)
	}
	items, err := store.ListInScopes(ctx, []ScopeRef{sessionScope("sess-1")}, Filter{})
	if err != nil {
		t.Fatalf("ListInScopes: %v", err)
	}
	if len(items) != 1 || items[0].Kind != KindStall {
		t.Fatalf("call 3: items = %v, want exactly one KindStall item", items)
	}
	if len(req.calls) != 0 {
		t.Fatalf("call 3: human seam already called (%v), want 0 (not at Human yet)", req.calls)
	}

	// Call 4: SupervisorTask's attempt budget is spent -> advances to
	// Human, this ticket's own notifier seam fires.
	if err := d.Observe(ctx, sig); err != nil {
		t.Fatalf("call 4: %v, want nil (human seam succeeds)", err)
	}
	if len(req.calls) != 1 {
		t.Fatalf("call 4: requester.calls = %v, want exactly 1", req.calls)
	}
	if req.calls[0].StallKind != StallKindBlocked {
		t.Errorf("call 4: notified StallKind = %s, want blocked", req.calls[0].StallKind)
	}

	// Call 5: the terminal guard makes a repeat a no-op — idempotency.
	err = d.Observe(ctx, sig)
	if !errors.Is(err, governor.EscalationExhausted) {
		t.Fatalf("call 5: err = %v, want EscalationExhausted", err)
	}
	if len(req.calls) != 1 {
		t.Fatalf("call 5: requester.calls = %v, want still exactly 1 (no duplicate human notification)", req.calls)
	}
}

// TestDetectorRecoveryCancelsInFlightEscalation proves that once
// progress resumes, the ladder is never advanced further for that
// session: the escalation that had reached rung Context never reaches
// SupervisorTask or Human, even though further Observe calls keep
// arriving.
func TestDetectorRecoveryCancelsInFlightEscalation(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	req := &fakeApprovalRequester{}
	d, store, clock := newTestDetector(t, now, fakeRetryer{err: cascade.New(cascade.KindUnavailable, "retry failed")}, fakeEnricher{err: cascade.New(cascade.KindUnavailable, "enrich failed")}, req)
	sig := StallSignal{Kind: SignalBlocked, SessionID: "sess-recover", At: now.UnixMilli()}

	_ = d.Observe(ctx, sig) // call 1: Retry fails -> Context
	_ = d.Observe(ctx, sig) // call 2: Context fails -> SupervisorTask

	// Recovery: progress resumes before the human step fires.
	d.Touch("sess-recover")

	// A further Observe call finds confidence healthy and does nothing:
	// Advance returns nil without executing SupervisorTask's seam.
	if err := d.Observe(ctx, sig); err != nil {
		t.Fatalf("post-recovery Observe: %v, want nil (escalation canceled)", err)
	}
	items, _ := store.ListInScopes(ctx, []ScopeRef{sessionScope("sess-recover")}, Filter{})
	if len(items) != 0 {
		t.Errorf("attention queue has %d items after recovery, want 0 (SupervisorTask never ran)", len(items))
	}
	if len(req.calls) != 0 {
		t.Errorf("requester.calls = %v after recovery, want 0 (Human never ran)", req.calls)
	}
	_ = clock
}

// TestDetectorPollReportsUnknownWhenSourceUnavailable proves Poll
// carries "could not tell" through to the recorded StallEvent as
// StallKindUnknown, distinct from the idle classification a genuinely
// stale-but-known session gets (TestDetectorPollClassifiesIdle below).
func TestDetectorPollReportsUnknownWhenSourceUnavailable(t *testing.T) {
	ctx := context.Background()
	req := &fakeApprovalRequester{}
	d, _, _ := newTestDetector(t, time.Now(), fakeRetryer{}, fakeEnricher{}, req)
	d.Touch("sess-unknown")
	d.tracker.MarkSourceUnavailable()

	if err := d.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	ev, ok := d.lookupStallEvent("sess-unknown")
	if !ok {
		t.Fatal("no StallEvent recorded for sess-unknown")
	}
	if ev.StallKind != StallKindUnknown {
		t.Errorf("StallKind = %s, want unknown", ev.StallKind)
	}
}

// TestDetectorPollClassifiesIdle proves a session with a KNOWN, stale
// last-progress timestamp classifies as StallKindIdle, not
// StallKindUnknown — the "not seen recently" vs "could not tell"
// distinction from the tracker's own side.
func TestDetectorPollClassifiesIdle(t *testing.T) {
	ctx := context.Background()
	req := &fakeApprovalRequester{}
	d, _, clock := newTestDetector(t, time.Now(), fakeRetryer{}, fakeEnricher{}, req)
	d.Touch("sess-idle")
	clock.Advance(2 * time.Minute)

	if err := d.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	ev, ok := d.lookupStallEvent("sess-idle")
	if !ok {
		t.Fatal("no StallEvent recorded for sess-idle")
	}
	if ev.StallKind != StallKindIdle {
		t.Errorf("StallKind = %s, want idle", ev.StallKind)
	}
	if ev.ElapsedSeconds <= 0 {
		t.Errorf("ElapsedSeconds = %d, want > 0", ev.ElapsedSeconds)
	}
}

// TestDetectorSetThresholdTakesEffect proves SetThreshold is not a dead
// setter: a session touched, then reclassified after SetThreshold
// shortens the window below its own elapsed time, reads as stalled.
func TestDetectorSetThresholdTakesEffect(t *testing.T) {
	d, _, clock := newTestDetector(t, time.Now(), fakeRetryer{}, fakeEnricher{}, &fakeApprovalRequester{})
	d.Touch("sess-thresh")
	clock.Advance(10 * time.Second)
	if err := d.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if ev, ok := d.lookupStallEvent("sess-thresh"); ok {
		t.Fatalf("lookupStallEvent = (%v, %v), want no event before threshold change (still healthy)", ev, ok)
	}

	d.SetThreshold(5 * time.Second)
	if err := d.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	ev, ok := d.lookupStallEvent("sess-thresh")
	if !ok || ev.StallKind != StallKindIdle {
		t.Fatalf("lookupStallEvent = (%v, %v), want idle after SetThreshold shortened the window", ev, ok)
	}
}

func TestProgressStatusString(t *testing.T) {
	cases := map[ProgressStatus]string{
		ProgressUnknown:    "unknown",
		ProgressHealthy:    "healthy",
		ProgressStalled:    "stalled",
		ProgressStatus(99): "unknown",
	}
	for status, want := range cases {
		if got := status.String(); got != want {
			t.Errorf("ProgressStatus(%d).String() = %q, want %q", status, got, want)
		}
	}
}

// TestDetectorObserveGateDeniedFiresOnlyOnThird proves Detector.Observe
// itself (not just the tracker) withholds escalation for the first two
// gate-denied signals and fires on the third, with the recorded
// StallEvent carrying StallKindGateDenied.
func TestDetectorObserveGateDeniedFiresOnlyOnThird(t *testing.T) {
	ctx := context.Background()
	req := &fakeApprovalRequester{}
	d, _, _ := newTestDetector(t, time.Now(), fakeRetryer{}, fakeEnricher{}, req)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()

	for i, offset := range []int64{0, 10, 20} {
		sig := StallSignal{Kind: SignalGateDenied, SessionID: "sess-gate", JobID: "job-x", At: base + offset*time.Minute.Milliseconds()}
		if err := d.Observe(ctx, sig); err != nil {
			t.Fatalf("Observe #%d: %v", i+1, err)
		}
		ev, ok := d.lookupStallEvent("sess-gate")
		if i < 2 {
			if ok {
				t.Fatalf("Observe #%d: lookupStallEvent = (%v, %v), want no event yet", i+1, ev, ok)
			}
			continue
		}
		if !ok || ev.StallKind != StallKindGateDenied {
			t.Fatalf("Observe #%d: lookupStallEvent = (%v, %v), want gate-denied", i+1, ev, ok)
		}
	}
}

func TestDetectorObserveRejectsInvalidSignal(t *testing.T) {
	d, _, _ := newTestDetector(t, time.Now(), fakeRetryer{}, fakeEnricher{}, &fakeApprovalRequester{})
	err := d.Observe(context.Background(), StallSignal{Kind: SignalBlocked, SessionID: ""})
	if !errors.Is(err, ErrInvalidSignal) {
		t.Errorf("err = %v, want ErrInvalidSignal", err)
	}
}
