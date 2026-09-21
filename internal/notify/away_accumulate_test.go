package notify

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
)

// routeBusNotification pushes one notification through the real bus-decode
// path (NotificationRouter.handle), the way a live subscription would.
func routeBusNotification(router *NotificationRouter, kind events.EventKind, id, targetScope string) {
	router.handle(events.Event{
		Kind:    kind,
		Payload: events.EncodeNotificationPayload(events.NotificationPayload{ID: id, TargetScope: targetScope}),
	})
}

// TestAwayAccumulationNotDispatchedUntilReturn is acceptance criterion 2 on
// the bus-decoded path: a notification routed while Away is not fanned out.
func TestAwayAccumulationNotDispatchedUntilReturn(t *testing.T) {
	h := newTestAway(t, newFixedClockPtr(time.Unix(3000, 0)), stubAdmission{}, "node-1")
	sub, got := recordingSubscriber("s1", true)
	h.registry.Subscribe(sub, session("s1"))
	h.driveToAway(t)

	routeBusNotification(h.router, "backup.result", "n1", "s1")

	dispatcher := NewDispatcher(h.router.queues, h.registry, NewInbox(), h.clock, silentLogger())
	if n := dispatcher.Drain(context.Background()); n != 0 {
		t.Fatalf("Drain() during Away processed %d, want 0 (accumulated, not queued)", n)
	}
	if len(*got) != 0 {
		t.Fatalf("subscriber received %d notifications during Away, want 0", len(*got))
	}
	drained := h.router.DrainAccumulated()
	if len(drained) != 1 || drained[0].ID != "n1" {
		t.Fatalf("DrainAccumulated() = %+v, want exactly [n1]", drained)
	}
}

// TestAwayDirectDeliverDuringAwayIsAccumulated is the input the accumulation
// gate used to let through: a producer calling Notifier.Deliver directly at
// 02:00 while the operator is away. It must accumulate exactly like a
// bus-decoded event, because the gate sits at the single choke point every
// producer reaches (queueSet.enqueue), not on one of the two paths.
func TestAwayDirectDeliverDuringAwayIsAccumulated(t *testing.T) {
	h := newTestAway(t, newFixedClockPtr(time.Unix(3100, 0)), stubAdmission{}, "node-1")
	sub, got := recordingSubscriber("s1", true)
	h.registry.Subscribe(sub, session("s1", "proj-1"))
	h.driveToAway(t)

	err := h.notifier.Deliver(context.Background(), Notification{
		ID: "direct-1", Class: ClassScoped, TargetScope: "proj-1", Priority: PriorityHigh,
	})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	dispatcher := NewDispatcher(h.router.queues, h.registry, NewInbox(), h.clock, silentLogger())
	if n := dispatcher.Drain(context.Background()); n != 0 {
		t.Fatalf("Drain() after a direct Deliver during Away processed %d, want 0", n)
	}
	if len(*got) != 0 {
		t.Fatalf("subscriber received %d notifications from a direct Deliver during Away, want 0", len(*got))
	}
	drained := h.router.DrainAccumulated()
	if len(drained) != 1 || drained[0].ID != "direct-1" {
		t.Fatalf("DrainAccumulated() = %+v, want exactly [direct-1]", drained)
	}
}

// TestAwayStallNoticeReclassifiesUrgent is acceptance criterion 3 through the
// injected StallSource: a notice naming an accumulated notification bumps it
// to Urgent.
func TestAwayStallNoticeReclassifiesUrgent(t *testing.T) {
	h := newTestAway(t, newFixedClockPtr(time.Unix(4000, 0)), stubAdmission{}, "node-1")
	h.driveToAway(t)

	// backup.result maps to PriorityNormal (sourceKindMapping).
	routeBusNotification(h.router, "backup.result", "stuck-1", "s1")
	h.stalls.push(StallNotice{NotificationID: "stuck-1"})
	h.ac.Tick(context.Background())

	drained := h.router.DrainAccumulated()
	if len(drained) != 1 {
		t.Fatalf("DrainAccumulated() len = %d, want 1", len(drained))
	}
	if drained[0].Priority != PriorityUrgent {
		t.Fatalf("reclassified notification Priority = %v, want PriorityUrgent", drained[0].Priority)
	}
}

// TestAwayStallNoticeMatchingNothingLeavesOthersAlone is the negative half:
// a notice that matches nothing must not touch unrelated buffered
// priorities, and an empty notice must not be treated as handled.
func TestAwayStallNoticeMatchingNothingLeavesOthersAlone(t *testing.T) {
	h := newTestAway(t, newFixedClockPtr(time.Unix(4100, 0)), stubAdmission{}, "node-1")
	h.driveToAway(t)

	routeBusNotification(h.router, "backup.result", "other-1", "s1")
	h.stalls.push(StallNotice{NotificationID: "no-match"}, StallNotice{})
	h.ac.Tick(context.Background())

	drained := h.router.DrainAccumulated()
	if len(drained) != 1 || drained[0].Priority != PriorityNormal {
		t.Fatalf("unrelated notification changed by a non-matching stall notice: %+v", drained)
	}
}

// TestAwayStallNoticeOutsideAwayChangesNothing proves a stall observed while
// Active has nothing accumulated to reclassify and does not reach the buffer.
func TestAwayStallNoticeOutsideAwayChangesNothing(t *testing.T) {
	h := newTestAway(t, newFixedClockPtr(time.Unix(4200, 0)), stubAdmission{}, "node-1")
	h.router.SetAccumulate(true) // buffer populated without entering Away
	routeBusNotification(h.router, "backup.result", "quiet-1", "s1")
	h.router.SetAccumulate(false)

	h.stalls.push(StallNotice{NotificationID: "quiet-1"})
	if got := h.ac.Tick(context.Background()); got == StateAway {
		t.Fatal("Tick entered Away unexpectedly")
	}
	drained := h.router.DrainAccumulated()
	if len(drained) != 1 || drained[0].Priority != PriorityNormal {
		t.Fatalf("stall notice outside Away reclassified a buffered item: %+v", drained)
	}
}

// TestAwayReturnDeliversAddressedDigestToItsOwnSession is acceptance
// criterion 4 with the privacy fix in it: the digest that reaches a session
// is ADDRESSED to that session (Class addressed, TargetSession, Visibility
// private), not a global-critical broadcast every other session also
// receives.
func TestAwayReturnDeliversAddressedDigestToItsOwnSession(t *testing.T) {
	h := newTestAway(t, newFixedClockPtr(time.Unix(5000, 0)), stubAdmission{}, "node-9")
	sub, got := recordingSubscriber("s1", true)
	h.registry.Subscribe(sub, session("s1", "proj-1"))
	h.driveToAway(t)
	routeBusNotification(h.router, "backup.result", "n1", "proj-1")

	h.ac.HandleEvent(context.Background(), ActivityEvent{At: h.clock.Now()})

	dispatcher := NewDispatcher(h.router.queues, h.registry, NewInbox(), h.clock, silentLogger())
	dispatcher.Drain(context.Background())

	if len(*got) != 1 {
		t.Fatalf("subscriber got %d notifications after return, want exactly 1 digest", len(*got))
	}
	d := (*got)[0]
	if d.Class != ClassAddressed || d.TargetSession != "s1" || d.Visibility != VisibilityPrivate {
		t.Fatalf("digest addressing = Class:%v TargetSession:%q Visibility:%v, want addressed/s1/private",
			d.Class, d.TargetSession, d.Visibility)
	}
	if d.Priority != PriorityUrgent || !d.ExpiresAt.IsZero() || d.OriginScope != "node-9" {
		t.Fatalf("digest fields = Priority:%v ExpiresAt:%v OriginScope:%q", d.Priority, d.ExpiresAt, d.OriginScope)
	}
	var p digestPayload
	if err := json.Unmarshal(d.Payload, &p); err != nil {
		t.Fatalf("digest payload not valid JSON: %v", err)
	}
	if p.Summary == "" || p.Withheld != 0 {
		t.Fatalf("digest payload = %+v, want a summary and zero withheld", p)
	}
	if h.router.Accumulating() {
		t.Fatal("accumulation still on after return")
	}
	if buf := h.router.DrainAccumulated(); len(buf) != 0 {
		t.Fatalf("accumulation buffer not cleared after the digest: %d left", len(buf))
	}
}
