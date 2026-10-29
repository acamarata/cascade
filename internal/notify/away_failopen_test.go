package notify

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/journal"
)

// seamPanics arms a panic inside each injected seam the controller calls
// while it holds its mutex.
type seamPanics struct {
	presence, admission, journalAppend atomic.Bool
	inner                              *awayHarness
}

func (s *seamPanics) LastActivity() time.Time {
	if s.presence.Load() {
		panic("presence seam died")
	}
	return s.inner.presence.LastActivity()
}

func (s *seamPanics) Inflight() int {
	if s.admission.Load() {
		panic("admission seam died")
	}
	return 0
}
func (s *seamPanics) QueueDepth() int { return 0 }

type panicJournal struct {
	journal.Store
	armed *atomic.Bool
}

func (p panicJournal) Append(ctx context.Context, e string, k journal.Kind, op string, pl json.RawMessage) (journal.Entry, error) {
	if p.armed.Load() {
		panic("journal seam died")
	}
	return p.Store.Append(ctx, e, k, op, pl)
}

// within fails the test when f has not returned inside the bound.
func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s blocked: the controller mutex is still held after the panic", what)
	}
}

// recoverPanic runs f and returns what it panicked with, nil if it did not.
func recoverPanic(f func()) (v any) {
	defer func() { v = recover() }()
	f()
	return nil
}

// TestAwayFailOpenAfterPanicInsideLockedSeam: a panic in the presence,
// admission or journal seam, called while Tick holds the controller mutex,
// must not strand the router. FailOpen (what the daemon runs from its
// recover) returns, delivery is on again, the held notification is back in
// the normal queue, and the controller is not left locked.
func TestAwayFailOpenAfterPanicInsideLockedSeam(t *testing.T) {
	cases := []struct {
		name    string
		arm     func(*seamPanics)
		trigger func(*awayHarness)
	}{
		{"presence", func(s *seamPanics) { s.presence.Store(true) }, func(*awayHarness) {}},
		{"admission", func(s *seamPanics) { s.admission.Store(true) }, func(*awayHarness) {}},
		{"journal-return", func(s *seamPanics) { s.journalAppend.Store(true) },
			func(h *awayHarness) { h.presence.set(h.clock.Now()) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, seams := newPanicSeamAway(t)
			h.driveToAway(t)
			routeBusNotification(h.router, "backup.result", "held-1", "proj-1")
			if n := h.router.queues.accumulatedLen(); n != 1 {
				t.Fatalf("setup: %d notifications held, want 1", n)
			}
			tc.arm(seams)
			tc.trigger(h)
			if v := recoverPanic(func() { h.ac.Tick(context.Background()) }); v == nil {
				t.Fatal("the armed seam did not panic inside Tick")
			}
			within(t, "FailOpen", h.ac.FailOpen)
			if h.router.Accumulating() {
				t.Fatal("FailOpen left the router withholding delivery")
			}
			if got := len(h.router.queues.queues[PriorityNormal]); got != 1 {
				t.Fatalf("normal queue holds %d after FailOpen, want the 1 held notification", got)
			}
			within(t, "State()", func() { h.ac.State() })
		})
	}
}

// TestAwayFailOpenAfterJournalPanicOnEntry: the journal panics while Tick is
// entering Away, after accumulation went on.
func TestAwayFailOpenAfterJournalPanicOnEntry(t *testing.T) {
	h, seams := newPanicSeamAway(t)
	h.clock.Advance(11 * time.Minute)
	h.ac.Tick(context.Background())
	seams.journalAppend.Store(true)
	if v := recoverPanic(func() { h.ac.Tick(context.Background()) }); v == nil {
		t.Fatal("the journal seam did not panic while entering Away")
	}
	routeBusNotification(h.router, "backup.result", "held-3", "proj-1") // accumulation is on, the episode never started cleanly
	within(t, "FailOpen", h.ac.FailOpen)
	if h.router.Accumulating() {
		t.Fatal("FailOpen left the router withholding delivery")
	}
	if got := len(h.router.queues.queues[PriorityNormal]); got != 1 {
		t.Fatalf("normal queue holds %d after FailOpen, want the 1 held notification", got)
	}
	within(t, "State()", func() { h.ac.State() })
}

// TestAwayDigestPanicRequeuesDrainedEpisode: a panic inside the digest
// compile, after the return drained the buffer, loses nothing.
func TestAwayDigestPanicRequeuesDrainedEpisode(t *testing.T) {
	h, _ := newPanicSeamAway(t)
	h.deps.Digest = &DigestCompiler{} // nil registry: CompileDrained panics
	h.ac = NewAwayController(h.deps, h.cfg)
	h.driveToAway(t)
	routeBusNotification(h.router, "backup.result", "held-4", "proj-1")
	if v := recoverPanic(func() { h.ac.HandleEvent(context.Background(), ActivityEvent{At: h.clock.Now()}) }); v == nil {
		t.Fatal("the digest compile did not panic")
	}
	if got := len(h.router.queues.queues[PriorityNormal]); got != 1 {
		t.Fatalf("normal queue holds %d, want the 1 drained notification requeued", got)
	}
	within(t, "State()", func() { h.ac.State() })
}

// TestAwayHandleEventPanicDoesNotLockController: HandleEvent shares the
// panic-safe critical section.
func TestAwayHandleEventPanicDoesNotLockController(t *testing.T) {
	h, seams := newPanicSeamAway(t)
	h.driveToAway(t)
	routeBusNotification(h.router, "backup.result", "held-2", "proj-1")
	seams.journalAppend.Store(true)
	if v := recoverPanic(func() { h.ac.HandleEvent(context.Background(), ActivityEvent{At: h.clock.Now()}) }); v == nil {
		t.Fatal("the journal seam did not panic inside HandleEvent")
	}
	within(t, "State()", func() { h.ac.State() })
	within(t, "FailOpen", h.ac.FailOpen)
	if got := len(h.router.queues.queues[PriorityNormal]); got != 1 {
		t.Fatalf("normal queue holds %d, want the 1 notification the failed return left held", got)
	}
}

// newPanicSeamAway builds a controller whose presence, admission and journal
// seams panic on demand.
func newPanicSeamAway(t *testing.T) (*awayHarness, *seamPanics) {
	t.Helper()
	h := newAwayFixture(t, newFixedClockPtr(time.Unix(9000, 0)), stubAdmission{}, "node-seams")
	s := &seamPanics{inner: h}
	h.deps.Presence = s
	h.deps.Admission = s
	h.deps.Journal = panicJournal{Store: h.journal, armed: &s.journalAppend}
	h.ac = NewAwayController(h.deps, h.cfg)
	return h, s
}

// TestAwayReturnWithoutDigestRequeuesHeldEpisode: with no digest compiler
// wired, the return still empties the held episode into the normal queue, and
// a zero-time activity event resolves against the injected clock.
func TestAwayReturnWithoutDigestRequeuesHeldEpisode(t *testing.T) {
	h := newAwayFixture(t, newFixedClockPtr(time.Unix(9000, 0)), stubAdmission{}, "node-nodigest")
	h.deps.Digest = nil
	h.ac = NewAwayController(h.deps, h.cfg)
	h.driveToAway(t)
	routeBusNotification(h.router, "backup.result", "held-5", "proj-1")
	if got := h.ac.HandleEvent(context.Background(), ActivityEvent{}); got != StateActive {
		t.Fatalf("HandleEvent state = %v, want StateActive", got)
	}
	if h.router.Accumulating() {
		t.Fatal("the return left the router withholding delivery")
	}
	if got := len(h.router.queues.queues[PriorityNormal]); got != 1 {
		t.Fatalf("normal queue holds %d, want the 1 held notification returned without a digest", got)
	}
	// The zero-time event counted as activity at "now": just under the
	// threshold later the machine is still Active, not pending.
	h.clock.Advance(9 * time.Minute)
	if got := h.ac.Tick(context.Background()); got != StateActive {
		t.Fatalf("Tick after the zero-time event = %v, want StateActive", got)
	}
}

// TestNewAwayControllerFromNilLogFallsBack: a nil logger is replaced, never
// dereferenced. Restoring Away with no episode id is the path that logs.
func TestNewAwayControllerFromNilLogFallsBack(t *testing.T) {
	h := newAwayFixture(t, newFixedClockPtr(time.Unix(9000, 0)), stubAdmission{}, "node-nolog")
	h.deps.Log = nil
	c := NewAwayControllerFrom(h.deps, h.cfg, ReplayResult{State: StateAway})
	if got := c.State(); got != StateActive {
		t.Fatalf("State = %v, want an anonymous Away episode refused as StateActive", got)
	}
	if h.router.Accumulating() {
		t.Fatal("an anonymous Away episode turned accumulation on")
	}
}

// replayFailJournal fails every Replay, as a journal whose store is down.
type replayFailJournal struct{ journal.Store }

func (replayFailJournal) Replay(context.Context, string, journal.Cursor, []journal.Kind) ([]journal.Entry, error) {
	return nil, errors.New("journal unavailable")
}

// TestReplayStateJournalErrorStartsActive: an unreadable journal yields the
// Active state and the error; the daemon then records away mode as skipped.
func TestReplayStateJournalErrorStartsActive(t *testing.T) {
	h := newAwayFixture(t, newFixedClockPtr(time.Unix(9000, 0)), stubAdmission{}, "node-replayerr")
	got, err := ReplayState(context.Background(), replayFailJournal{Store: h.journal}, "node-replayerr")
	if err == nil || err.Error() != "journal unavailable" {
		t.Fatalf("ReplayState error = %v, want the journal error unchanged", err)
	}
	if got.State != StateActive {
		t.Fatalf("ReplayState state = %v, want StateActive", got.State)
	}
}
