// Purpose: pin the visibility-deadline semantics the recovery and sweep
//   paths share: enqueue order survives an expired claim at recovery, a
//   claim expires exactly AT its deadline (not one tick after), and an Ack
//   with the receipt of an expired claim is refused without touching the
//   stored record. Errors are checked by exact kind AND full message.
// SPORT: internal.storage.queue.Queue/CHANGED.

package queue_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/queue"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestQueueRecoveryRedeliversExpiredClaimInEnqueueOrder: A is enqueued
// before B and claimed; its claim expires while no process is running. A
// fresh Queue over the same store must offer A first (its enqueue
// position), not B.
func TestQueueRecoveryRedeliversExpiredClaimInEnqueueOrder(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteFault(t)
	q1 := queue.New(f.driver, f.clock, queue.Config{})
	idA, err := q1.Enqueue(ctx, "ns", []byte("A"))
	requireNoErr(t, err, "Enqueue A")
	idB, err := q1.Enqueue(ctx, "ns", []byte("B"))
	requireNoErr(t, err, "Enqueue B")
	claimed, err := q1.Dequeue(ctx, "ns", time.Minute)
	requireNoErr(t, err, "claim A")
	if claimed == nil || claimed.ID != idA {
		t.Fatalf("first Dequeue = %+v, want A (%q)", claimed, idA)
	}

	f.clock.Advance(2 * time.Minute)
	q2 := queue.New(f.driver, f.clock, queue.Config{})
	first, err := q2.Dequeue(ctx, "ns", time.Minute)
	requireNoErr(t, err, "recovered first Dequeue")
	if first == nil || first.ID != idA {
		t.Fatalf("recovered first Dequeue = %+v, want expired-claim A (%q) before B", first, idA)
	}
	second, err := q2.Dequeue(ctx, "ns", time.Minute)
	requireNoErr(t, err, "recovered second Dequeue")
	if second == nil || second.ID != idB {
		t.Fatalf("recovered second Dequeue = %+v, want B (%q)", second, idB)
	}
}

// TestQueueClaimExpiresExactlyAtDeadline: the visibility window has
// "elapsed" at now == deadline, the same boundary recovery uses. One
// nanosecond earlier the claim is still invisible; at the deadline it is
// redelivered under a new receipt and the old receipt is refused.
func TestQueueClaimExpiresExactlyAtDeadline(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteFault(t)
	q := queue.New(f.driver, f.clock, queue.Config{})
	id, err := q.Enqueue(ctx, "ns", []byte("payload"))
	requireNoErr(t, err, "Enqueue")
	old, err := q.Dequeue(ctx, "ns", time.Minute)
	requireNoErr(t, err, "claim")

	f.clock.Advance(time.Minute - time.Nanosecond)
	early, err := q.Dequeue(ctx, "ns", time.Minute)
	requireNoErr(t, err, "Dequeue one tick before the deadline")
	if early != nil {
		t.Fatalf("Dequeue before the deadline = %+v, want nil", early)
	}

	f.clock.Advance(time.Nanosecond) // now == deadline
	again, err := q.Dequeue(ctx, "ns", time.Minute)
	requireNoErr(t, err, "Dequeue at the deadline")
	if again == nil || again.ID != id || again.Receipt == old.Receipt {
		t.Fatalf("Dequeue at now == deadline = %+v, want %q redelivered under a new receipt (old %q)", again, id, old.Receipt)
	}
}

// TestQueueAckAfterExpiryIsTimeoutAndKeepsRecord: an Ack whose claim
// deadline has passed is refused with the exact stale-receipt Timeout and
// the message stays stored for redelivery.
func TestQueueAckAfterExpiryIsTimeoutAndKeepsRecord(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteFault(t)
	q := queue.New(f.driver, f.clock, queue.Config{})
	id, err := q.Enqueue(ctx, "ns", []byte("payload"))
	requireNoErr(t, err, "Enqueue")
	msg, err := q.Dequeue(ctx, "ns", time.Minute)
	requireNoErr(t, err, "claim")
	held := f.stored("msg:" + id)

	f.clock.Advance(2 * time.Minute)
	ackErr := q.Ack(ctx, "ns", msg.Receipt)
	requireKindMsg(t, ackErr, cascade.KindTimeout,
		fmt.Sprintf("queue.Ack: receipt %q in namespace %q is stale or unknown", msg.Receipt, "ns"), "Ack after expiry")
	if f.stored("msg:"+id) == nil {
		t.Fatal("Ack after expiry removed the record")
	}
	f.requireUnchanged("msg:"+id, held, "after refused expired Ack")
}
