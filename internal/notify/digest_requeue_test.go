package notify

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// cancelAfterFirstDigestClock is a runtime.Clock that cancels the Compile
// context once the FIRST session's digest has been built. DigestCompiler reads
// its clock exactly once per session (buildFor's Timestamp), so this makes the
// REAL Notifier.DeliverNow — whose first act is a ctx.Err() check — fail for
// the SECOND session only. That is the per-session partial failure a real
// notifier cannot otherwise be made to produce, and driving it this way runs
// Compile's own loop and accounting rather than a copy of them.
type cancelAfterFirstDigestClock struct {
	now    time.Time
	calls  int
	cancel context.CancelFunc
}

func (c *cancelAfterFirstDigestClock) Now() time.Time {
	c.calls++
	if c.calls == 2 {
		c.cancel()
	}
	return c.now
}

// TestDigestWithheldItemsAreRequeuedNotDestroyed is the silent discard on the
// SUCCESS path. Input: Away, one accumulated item targeting project-3, and
// only a project-1 session registered. The item is withheld from the one
// digest that exists -- it must be RE-QUEUED, so it is still delivered once a
// project-3 session appears. The "1 withheld" count reports it; it does not
// consume it.
func TestDigestWithheldItemsAreRequeuedNotDestroyed(t *testing.T) {
	dc, router, registry := newTestDigestCompiler(t, time.Unix(2000, 0), DefaultAwayConfig())
	sub1, got1 := recordingSubscriber("sub-1", true)
	registry.Subscribe(sub1, session("s1", "project-1"))

	seedAccumulatedFromBus(router, "backup.result", "p3-item", "project-3")

	if err := dc.Compile(context.Background(), "ep", "node-x"); err != nil {
		t.Fatalf("Compile: %v", err)
	}

	// The withheld original is back in the NORMAL queue, intact: same id,
	// same scope, same priority, so the ordinary dispatch path gates it again
	// exactly as it would an un-accumulated notification. It was not consumed
	// by the digest that merely counted it.
	if len(router.queues.queues[PriorityNormal]) != 1 {
		t.Fatal("the withheld original was destroyed instead of being re-queued")
	}
	requeued := <-router.queues.queues[PriorityNormal]
	if requeued.ID != "p3-item" || requeued.TargetScope != "project-3" || requeued.Priority != PriorityNormal {
		t.Fatalf("re-queued original = %+v, want p3-item/project-3/Normal unchanged", requeued)
	}

	// And the session that never was eligible sees only an opaque count.
	NewDispatcher(router.queues, registry, NewInbox(), fixedClock{now: time.Unix(2000, 0)}, silentLogger()).
		Drain(context.Background())
	if len(*got1) != 1 {
		t.Fatalf("s1 got %d digests, want 1", len(*got1))
	}
	var p digestPayload
	if err := json.Unmarshal((*got1)[0].Payload, &p); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if p.Withheld != 1 || len(p.UrgentDeepLinks) != 0 {
		t.Fatalf("s1 payload = %+v, want one opaque withheld and no links", p)
	}
}

// TestDigestPartialFailureRequeuesOnlyTheFailedSessionsItems is the
// double-count the all-or-nothing re-queue produced: sa's digest delivered and
// already counts its items, sb's failed. Only the items no delivered digest
// represented may be re-queued. The failure is injected at the real delivery
// seam (the clock cancels the context after sa's digest is built), so this
// drives DigestCompiler's own loop and accounting end to end.
func TestDigestPartialFailureRequeuesOnlyTheFailedSessionsItems(t *testing.T) {
	now := time.Unix(2100, 0)
	queues := newQueueSet(DefaultConfig(), silentLogger())
	registry := NewRegistry()
	router := NewNotificationRouter(queues, fixedClock{now: now}, silentLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk := &cancelAfterFirstDigestClock{now: now, cancel: cancel}
	dc := NewDigestCompiler(registry, NewNotifier(queues, fixedClock{now: now}), router, clk, DefaultAwayConfig(), silentLogger())

	subA, gotA := recordingSubscriber("sub-a", true)
	subB, gotB := recordingSubscriber("sub-b", true)
	registry.Subscribe(subA, session("sa", "scope-a")) // candidateSessions sorts by id: sa first
	registry.Subscribe(subB, session("sb", "scope-b"))

	seedAccumulated(router,
		Notification{ID: "a-item", Class: ClassScoped, TargetScope: "scope-a", Priority: PriorityNormal},
		Notification{ID: "b-item", Class: ClassScoped, TargetScope: "scope-b", Priority: PriorityNormal},
		Notification{ID: "shared", Class: ClassGlobalCritical, Priority: PriorityHigh},
	)

	if err := dc.Compile(ctx, "ep", "node-x"); err == nil {
		t.Fatal("Compile returned nil although sb's digest delivery failed")
	}

	if n := len(router.queues.queues[PriorityNormal]); n != 1 {
		t.Fatalf("normal queue holds %d, want only b-item re-queued", n)
	}
	requeued := <-router.queues.queues[PriorityNormal]
	if requeued.ID != "b-item" {
		t.Fatalf("re-queued %q, want b-item (a-item is already counted in sa's delivered digest)", requeued.ID)
	}
	if len(router.queues.queues[PriorityHigh]) != 0 {
		t.Fatal("the shared item was re-queued although sa's delivered digest already counts it")
	}

	// And sa's digest really was delivered, while sb's never existed.
	NewDispatcher(queues, registry, NewInbox(), fixedClock{now: now}, silentLogger()).
		Drain(context.Background())
	if len(*gotA) != 1 || (*gotA)[0].ID != "ep:sa" {
		t.Fatalf("sa's subscriber got %+v, want exactly its own digest ep:sa", *gotA)
	}
	if len(*gotB) != 0 {
		t.Fatalf("sb's subscriber got %+v, want nothing: its digest failed to dispatch", *gotB)
	}
}

// TestDigestReQueueOnDispatchFailure is acceptance criterion 5's total-failure
// case: when every digest Deliver fails (an already-canceled context, which
// Notifier.Deliver's own ctx.Err() check refuses), every accumulated original
// is re-queued, not dropped.
func TestDigestReQueueOnDispatchFailure(t *testing.T) {
	dc, router, registry := newTestDigestCompiler(t, time.Unix(2200, 0), DefaultAwayConfig())
	sub, _ := recordingSubscriber("s1", true)
	registry.Subscribe(sub, session("s1", "scope-1"))

	seedAccumulatedFromBus(router, "backup.result", "orig-1", "scope-1")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled before Compile runs

	if err := dc.Compile(ctx, "ep-fail", "node-x"); err == nil {
		t.Fatal("Compile with a canceled context returned nil, want the Deliver failure")
	}
	if drained := router.DrainAccumulated(); len(drained) != 0 {
		t.Fatalf("accumulation buffer still holds %d, want 0 (moved to the normal queue)", len(drained))
	}
	if len(router.queues.queues[PriorityNormal]) != 1 {
		t.Fatal("the re-queued original is not in the router's normal PriorityNormal queue")
	}
}

// TestDigestNoSessionsRequeuesWithoutError proves the zero-candidate-session
// path never drops the accumulated original and never reports an error.
func TestDigestNoSessionsRequeuesWithoutError(t *testing.T) {
	dc, router, _ := newTestDigestCompiler(t, time.Unix(2300, 0), DefaultAwayConfig())
	seedAccumulatedFromBus(router, "backup.result", "orphan-1", "scope-1")

	if err := dc.Compile(context.Background(), "ep", "node-x"); err != nil {
		t.Fatalf("Compile with zero sessions: %v", err)
	}
	if len(router.queues.queues[PriorityNormal]) != 1 {
		t.Fatal("the orphaned accumulated notification was not re-queued when no session is registered")
	}
}

// TestDigestClearsAccumulationBuffer proves Compile always empties the buffer:
// every drained item is either represented in a delivered digest or back in
// the normal queue, never still buffered.
func TestDigestClearsAccumulationBuffer(t *testing.T) {
	dc, router, registry := newTestDigestCompiler(t, time.Unix(2400, 0), DefaultAwayConfig())
	sub, _ := recordingSubscriber("s1", true)
	registry.Subscribe(sub, session("s1", "scope-1"))

	seedAccumulatedFromBus(router, "backup.result", "n1", "scope-1")

	if err := dc.Compile(context.Background(), "ep", "node-x"); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if buf := router.DrainAccumulated(); len(buf) != 0 {
		t.Fatalf("accumulation buffer not cleared: %d left", len(buf))
	}
	if len(router.queues.queues[PriorityNormal]) != 0 {
		t.Fatal("an item represented in a delivered digest was also re-queued (double count)")
	}
}

// TestDigestRefusesWhileAccumulationIsStillOn is the trap the digest's own
// delivery path sets for it: delivered through the ordinary producer path with
// the gate still closed, the return summary would be buffered instead of
// dispatched and reach nobody. Compile must refuse before draining, leaving
// the buffer intact for a correct call.
func TestDigestRefusesWhileAccumulationIsStillOn(t *testing.T) {
	dc, router, registry := newTestDigestCompiler(t, time.Unix(2600, 0), DefaultAwayConfig())
	sub, got := recordingSubscriber("s1", true)
	registry.Subscribe(sub, session("s1", "scope-1"))

	router.SetAccumulate(true)
	routeBusNotification(router, "backup.result", "still-buffered", "scope-1")

	err := dc.Compile(context.Background(), "ep", "node-x")
	if err == nil {
		t.Fatal("Compile while accumulation is on returned nil, want a refusal")
	}
	if len(*got) != 0 {
		t.Fatalf("a digest was built while accumulation was on: %+v", *got)
	}
	buf := router.DrainAccumulated()
	if len(buf) != 1 || buf[0].ID != "still-buffered" {
		t.Fatalf("buffer after the refusal = %+v, want the untouched accumulated item", buf)
	}
}

// TestDigestRequeueBypassesTheAccumulationGate is the re-queue's own trap: a
// re-queue that went back through the gate while accumulation is still on
// would put the item straight back into the buffer it just left.
func TestDigestRequeueBypassesTheAccumulationGate(t *testing.T) {
	_, router, _ := newTestDigestCompiler(t, time.Unix(2500, 0), DefaultAwayConfig())
	router.SetAccumulate(true)
	router.requeue(Notification{ID: "back", Class: ClassGlobalCritical, Priority: PriorityNormal})

	if len(router.queues.queues[PriorityNormal]) != 1 {
		t.Fatal("requeue did not reach the normal queue while accumulation was on")
	}
	if buf := router.DrainAccumulated(); len(buf) != 0 {
		t.Fatalf("requeue re-accumulated %d items instead of returning them to the queue", len(buf))
	}
}
