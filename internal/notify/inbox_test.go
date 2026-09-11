package notify

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNotifierDeliverValidatesScopedFields(t *testing.T) {
	queues := newQueueSet(DefaultConfig(), silentLogger())
	nf := NewNotifier(queues, fixedClock{now: time.Unix(1, 0)})

	err := nf.Deliver(context.Background(), Notification{ID: "n1", Class: ClassScoped})
	if !errors.Is(err, errMissingScopeFields) {
		t.Fatalf("Deliver with no scope fields = %v, want errMissingScopeFields", err)
	}
}

func TestNotifierDeliverValidatesAddressedFields(t *testing.T) {
	queues := newQueueSet(DefaultConfig(), silentLogger())
	nf := NewNotifier(queues, fixedClock{now: time.Unix(1, 0)})

	err := nf.Deliver(context.Background(), Notification{ID: "n1", Class: ClassAddressed})
	if !errors.Is(err, errMissingScopeFields) {
		t.Fatalf("Deliver addressed with no target session = %v, want errMissingScopeFields", err)
	}
}

func TestNotifierDeliverAssignsTimestampAndEnqueues(t *testing.T) {
	queues := newQueueSet(DefaultConfig(), silentLogger())
	clk := fixedClock{now: time.Unix(42, 0)}
	nf := NewNotifier(queues, clk)

	err := nf.Deliver(context.Background(), Notification{
		ID: "n1", Class: ClassGlobalCritical, Priority: PriorityUrgent,
	})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	select {
	case n := <-queues.queues[PriorityUrgent]:
		if !n.Timestamp.Equal(clk.now) {
			t.Fatalf("Timestamp = %v, want %v (injected clock)", n.Timestamp, clk.now)
		}
	default:
		t.Fatal("Deliver did not enqueue the notification")
	}
}

func TestInboxListReadAckWithheldAndUnknown(t *testing.T) {
	inbox := NewInbox()
	now := time.Unix(1000, 0)
	sess := session("s1", "project-1")

	n := Notification{ID: "n1", Class: ClassScoped, TargetScope: "project-1", Visibility: VisibilityScoped}
	inbox.record(n)

	got, err := inbox.Read(context.Background(), sess, "n1")
	if err != nil || got.ID != "n1" {
		t.Fatalf("Read visible notification: got=%v err=%v", got, err)
	}

	if _, err := inbox.Read(context.Background(), sess, "does-not-exist"); !errors.Is(err, ErrNotificationUnknown) {
		t.Fatalf("Read unknown id = %v, want ErrNotificationUnknown", err)
	}

	other := session("s2", "project-9")
	if _, err := inbox.Read(context.Background(), other, "n1"); !errors.Is(err, ErrNotificationWithheld) {
		t.Fatalf("Read out-of-scope id = %v, want ErrNotificationWithheld", err)
	}

	if err := inbox.Ack(context.Background(), sess, "n1"); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if err := inbox.Ack(context.Background(), sess, "n1"); !errors.Is(err, ErrNotificationAlreadyAcked) {
		t.Fatalf("second Ack = %v, want ErrNotificationAlreadyAcked", err)
	}

	list, counts := inbox.List(context.Background(), sess, InboxOpts{Unread: true})
	if len(list) != 0 {
		t.Fatalf("Unread-only List after Ack returned %d records, want 0", len(list))
	}
	if counts.Total != 1 {
		t.Fatalf("Counts.Total = %d, want 1", counts.Total)
	}
	_ = now
}

func TestInboxExpireSweepsAndHidesFromList(t *testing.T) {
	inbox := NewInbox()
	now := time.Unix(2000, 0)
	sess := session("s1")

	n := Notification{ID: "n1", Class: ClassGlobalCritical, TargetSession: "s1", ExpiresAt: now.Add(-time.Minute)}
	inbox.record(n)

	if _, err := inbox.Read(context.Background(), sess, "n1"); err != nil {
		t.Fatalf("Read before Expire sweep: %v", err)
	}

	swept := inbox.Expire(now)
	if swept != 1 {
		t.Fatalf("Expire swept %d, want 1", swept)
	}
	if _, err := inbox.Read(context.Background(), sess, "n1"); !errors.Is(err, ErrNotificationExpired) {
		t.Fatalf("Read after Expire = %v, want ErrNotificationExpired", err)
	}
	_, counts := inbox.List(context.Background(), sess, InboxOpts{})
	if counts.Expired != 1 {
		t.Fatalf("Counts.Expired = %d, want 1", counts.Expired)
	}
}

func TestInboxListAllExpandsToSharedView(t *testing.T) {
	inbox := NewInbox()
	n := Notification{ID: "n1", Class: ClassAddressed, TargetSession: "someone-else", Visibility: VisibilityShared}
	inbox.record(n)

	sess := session("me")
	list, _ := inbox.List(context.Background(), sess, InboxOpts{All: true})
	if len(list) != 1 {
		t.Fatalf("shared-visibility notification not returned under the --all view: got %d", len(list))
	}
}

// TestInboxIsProjectionNotRecordOfTruth (R-21.227): the Inbox holds a live,
// in-memory view only. A fresh Inbox (simulating a daemon restart, since
// this package persists nothing across process lifetime) starts empty even
// though a prior Inbox instance had recorded a Notification -- proving the
// router itself is not the record of truth. A producer needing
// pending-across-restart semantics must re-Deliver into a fresh Inbox on
// daemon start, which is exactly what re-recording below simulates.
func TestInboxIsProjectionNotRecordOfTruth(t *testing.T) {
	first := NewInbox()
	first.record(Notification{ID: "n1", Class: ClassGlobalCritical, TargetSession: "s1"})
	sess := session("s1")
	if _, err := first.Read(context.Background(), sess, "n1"); err != nil {
		t.Fatalf("Read from the original Inbox: %v", err)
	}

	// Simulate a daemon restart: a brand new Inbox, with nothing carried
	// over automatically.
	restarted := NewInbox()
	if _, err := restarted.Read(context.Background(), sess, "n1"); !errors.Is(err, ErrNotificationUnknown) {
		t.Fatalf("a restarted Inbox unexpectedly still knows n1: err=%v (the router must not be a record of truth)", err)
	}

	// A producer that owns pending-across-restart semantics re-Delivers
	// on daemon start; after that, the projection reflects it again.
	restarted.record(Notification{ID: "n1", Class: ClassGlobalCritical, TargetSession: "s1"})
	if _, err := restarted.Read(context.Background(), sess, "n1"); err != nil {
		t.Fatalf("Read after producer re-delivery: %v", err)
	}
}
