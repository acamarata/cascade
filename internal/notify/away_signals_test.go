package notify

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestAwayEmptyReturnDeliversNoDigestToAnySession is the F2 finding: an away
// episode that accumulated NOTHING has nothing to summarise, so it must
// deliver no digest at all. Input: the contract's own 30m threshold, 31
// minutes idle, not one notification queued while away, two registered
// sessions. A "no notifications while away" Priority-Urgent alert per session
// on every quiet return is noise, and it is what the compiler used to send.
func TestAwayEmptyReturnDeliversNoDigestToAnySession(t *testing.T) {
	ctx := context.Background()
	h := newAwayFixture(t, newFixedClockPtr(time.Unix(6000, 0)), stubAdmission{}, "node-quiet")
	h.cfg = DefaultAwayConfig() // away_threshold 30m, digest_urgent_deeplinks 10
	h.ac = NewAwayController(h.deps, h.cfg)
	subA, gotA := recordingSubscriber("a", true)
	subB, gotB := recordingSubscriber("b", true)
	h.registry.Subscribe(subA, session("sa", "proj-a"))
	h.registry.Subscribe(subB, session("sb", "proj-b"))

	h.clock.Advance(31 * time.Minute)
	h.ac.Tick(ctx)
	if state := h.ac.Tick(ctx); state != StateAway {
		t.Fatalf("Tick after 31 idle minutes = %v, want StateAway", state)
	}

	if state := h.ac.HandleEvent(ctx, ActivityEvent{At: h.clock.Now()}); state != StateActive {
		t.Fatalf("HandleEvent = %v, want StateActive", state)
	}
	NewDispatcher(h.router.queues, h.registry, NewInbox(), h.clock, silentLogger()).Drain(ctx)

	if len(*gotA) != 0 || len(*gotB) != 0 {
		t.Fatalf("an empty away episode delivered sa:%d sb:%d notifications, want none at all", len(*gotA), len(*gotB))
	}
}

// TestAwayStallNoticeArrivingWhileActiveIsNotConsumed is the first half of the
// F3 finding: the source was drained BEFORE the state was read, so a notice
// observed outside Away was consumed and then discarded with a WARN. The
// source's own contract delivers each notice exactly once, so consuming it
// while there is no accumulation buffer to apply it to DESTROYS it. AC#3
// obliges reclassification only while Away, so the honest behaviour is to
// leave such a notice pending for the next away episode — proven here by
// applying the very same notice in that next episode.
func TestAwayStallNoticeArrivingWhileActiveIsNotConsumed(t *testing.T) {
	ctx := context.Background()
	h := newTestAway(t, newFixedClockPtr(time.Unix(4300, 0)), stubAdmission{}, "node-1")

	h.stalls.push(StallNotice{NotificationID: "later-1"})
	if state := h.ac.Tick(ctx); state != StateActive {
		t.Fatalf("Tick before the threshold = %v, want StateActive", state)
	}
	if left := h.stalls.remaining(); left != 1 {
		t.Fatalf("a stall notice observed while Active left %d pending, want 1 "+
			"(consuming it here destroys it: the source delivers each notice exactly once)", left)
	}

	// The next away episode accumulates the notification the notice names,
	// and the still-pending notice is applied to it.
	h.driveToAway(t)
	routeBusNotification(h.router, "backup.result", "later-1", "proj-1") // backup.result maps to Normal
	h.ac.Tick(ctx)

	if left := h.stalls.remaining(); left != 0 {
		t.Fatalf("the pending notice was not consumed while Away: %d left", left)
	}
	drained := h.router.DrainAccumulated()
	if len(drained) != 1 || drained[0].Priority != PriorityUrgent {
		t.Fatalf("accumulated item after the deferred notice = %+v, want one PriorityUrgent", drained)
	}
}

// TestCompileDrainedDeliversPastAClosedGate is the s49t2-confirm2 MUTATION
// finding: no test proves CompileDrained's delivery survives a gate a NEW
// away episode has since closed. Input: accumulation switched back ON (as if
// away mode re-entered while episode A's digest is still being compiled),
// then CompileDrained runs on a drained snapshot for one registered session.
// The digest must land in that session's subscriber via the priority queue,
// not the reopened accumulation buffer, and the buffer must stay empty
// afterward. notifier.go's DeliverNow exists to bypass this exact gate
// (enqueueNow, not enqueue) — swapping that one call makes this test fail.
func TestCompileDrainedDeliversPastAClosedGate(t *testing.T) {
	dc, router, registry := newTestDigestCompiler(t, time.Unix(2700, 0), DefaultAwayConfig())
	sub, got := recordingSubscriber("s1", true)
	registry.Subscribe(sub, session("s1", "scope-1"))

	drained := []Notification{
		{ID: "orig-1", Class: ClassScoped, TargetScope: "scope-1", Priority: PriorityNormal},
	}

	router.SetAccumulate(true) // a new away episode reopened the gate

	if err := dc.CompileDrained(context.Background(), "ep-gate", "node-x", drained); err != nil {
		t.Fatalf("CompileDrained: %v", err)
	}

	if n := router.queues.accumulatedLen(); n != 0 {
		t.Fatalf("accumulation buffer holds %d after CompileDrained, want 0: "+
			"the digest was buffered into the reopened episode instead of delivered", n)
	}

	NewDispatcher(router.queues, registry, NewInbox(), fixedClock{now: time.Unix(2700, 0)}, silentLogger()).
		Drain(context.Background())
	if len(*got) != 1 {
		t.Fatalf("s1 got %d digests, want 1: CompileDrained must deliver past a closed gate", len(*got))
	}
}

// slowStallBox is a StallSource whose drain takes a measurable moment. It
// widens the window between "the notices have left the source" and "the
// controller decides what to do with them" from nanoseconds to milliseconds,
// and that window is the whole of F3's second half: a return landing inside it
// saw Active and dropped the notice with only a WARN. started is closed once
// the drain has taken the notices, so the test knows exactly when the window
// is open.
type slowStallBox struct {
	stallBox
	delay   time.Duration
	once    sync.Once
	started chan struct{}
}

func newSlowStallBox(delay time.Duration) *slowStallBox {
	return &slowStallBox{delay: delay, started: make(chan struct{})}
}

func (s *slowStallBox) DrainStallNotices() []StallNotice {
	out := s.stallBox.DrainStallNotices()
	if len(out) > 0 {
		s.once.Do(func() { close(s.started) })
		time.Sleep(s.delay)
	}
	return out
}

// TestAwayStallNoticeIsNotLostToAConcurrentReturn is F3's second half. The
// interleaving is forced, not hoped for: a Tick begins draining the stall
// source, and the Active-return is issued while that drain is still in
// flight. Consuming and applying inside the return transition's own lock is
// what makes this safe — the return cannot land between the consume and the
// reclassify, so it waits, and the notice is applied to the buffer it belongs
// to. With no session registered, the accumulated item is re-queued on return
// carrying whatever priority it then has, which makes the notice's fate
// directly observable: Urgent means applied, Normal means dropped.
func TestAwayStallNoticeIsNotLostToAConcurrentReturn(t *testing.T) {
	ctx := context.Background()
	h := newAwayFixture(t, newFixedClockPtr(time.Unix(4400, 0)), stubAdmission{}, "node-stall")
	slow := newSlowStallBox(50 * time.Millisecond)
	h.deps.Stalls = slow
	h.ac = NewAwayController(h.deps, h.cfg)

	h.driveToAway(t)
	routeBusNotification(h.router, "backup.result", "stuck-1", "proj-1")
	slow.push(StallNotice{NotificationID: "stuck-1"})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.ac.Tick(ctx)
	}()
	<-slow.started // the notices have left the source; the window is open
	h.ac.HandleEvent(ctx, ActivityEvent{At: h.clock.Now()})
	wg.Wait()

	if left := slow.remaining(); left != 0 {
		t.Fatalf("the consumed notice was put back: %d pending", left)
	}
	if n := len(h.router.queues.queues[PriorityUrgent]); n != 1 {
		t.Fatalf("Urgent queue holds %d, want the reclassified item: a return landing inside the drain "+
			"dropped the stall notice, and the item is still at its original priority (%d in Normal)",
			n, len(h.router.queues.queues[PriorityNormal]))
	}
}
