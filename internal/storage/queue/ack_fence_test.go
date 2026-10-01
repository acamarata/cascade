// Purpose: prove the Ack/Nack fences refuse the input a weak (unfenced)
//   implementation would accept: a stale claimant acting on a record that a
//   later claimant now owns, and a storage read failure inside the fenced
//   Tx. Errors are checked by exact kind AND full message, never by
//   errors.Is, which compares Kind only in this codebase.
// SPORT: internal.storage.queue.Queue/CHANGED.

package queue_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/queue"
	"github.com/acamarata/cascade/pkg/cascade"
)

// requireKindMsg fails unless err is exactly kind with the full message msg.
func requireKindMsg(t *testing.T, err error, kind cascade.Kind, msg, what string) {
	t.Helper()
	got, ok := cascade.KindOf(err)
	if err == nil || !ok || got != kind {
		t.Fatalf("%s: want kind %v, got %v", what, kind, err)
	}
	if want := kind.String() + ": " + msg; err.Error() != want {
		t.Fatalf("%s: message mismatch\n got: %s\nwant: %s", what, err.Error(), want)
	}
}

// TestQueueAckByStaleClaimantRefusedAfterReclaim: instance A holds a claim
// its own (lagging) clock still believes is live; instance B, whose clock
// is later, legitimately reclaimed the expired message. A's Ack must be
// refused with KindConflict and B's claim left byte-identical; A's Nack
// must not overwrite B's claim either. An unfenced delete would destroy
// B's live work.
func TestQueueAckByStaleClaimantRefusedAfterReclaim(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteFault(t)
	t0 := time.Unix(1_700_000_000, 0)
	qa := queue.New(f.driver, runtime.NewFixedClock(t0), queue.Config{})
	id, err := qa.Enqueue(ctx, "ns", []byte("payload"))
	requireNoErr(t, err, "Enqueue")
	a, err := qa.Dequeue(ctx, "ns", time.Minute)
	requireNoErr(t, err, "A Dequeue")

	qb := queue.New(f.driver, runtime.NewFixedClock(t0.Add(2*time.Minute)), queue.Config{})
	b, err := qb.Dequeue(ctx, "ns", time.Hour)
	requireNoErr(t, err, "B reclaim of the expired message")
	if b == nil || b.ID != id || b.Receipt == a.Receipt {
		t.Fatalf("B Dequeue = %+v, want a fresh claim of %q with a new receipt (A's was %q)", b, id, a.Receipt)
	}
	owned := f.stored("msg:" + id)

	ackErr := qa.Ack(ctx, "ns", a.Receipt)
	requireKindMsg(t, ackErr, cascade.KindConflict,
		fmt.Sprintf("queue.Ack: message %q in namespace %q changed since claim (receipt %q refused)", id, "ns", a.Receipt), "stale Ack")
	f.requireUnchanged("msg:"+id, owned, "after refused stale Ack")

	nackErr := qa.Nack(ctx, "ns", a.Receipt)
	requireKindMsg(t, nackErr, cascade.KindTimeout,
		fmt.Sprintf("queue.Nack: receipt %q in namespace %q is stale or unknown", a.Receipt, "ns"), "stale Nack")
	f.requireUnchanged("msg:"+id, owned, "after refused stale Nack")

	requireNoErr(t, qb.Ack(ctx, "ns", b.Receipt), "B Ack still succeeds")
	if f.stored("msg:"+id) != nil {
		t.Fatal("B's Ack left the record behind")
	}
}

// TestQueueAckStorageReadFailureIsNotConflict: a failed read inside the
// fenced Tx says nothing about the record, so it must surface as
// KindUnavailable (retryable), never KindConflict, and leave the claim
// intact so the same receipt can be retried.
func TestQueueAckStorageReadFailureIsNotConflict(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteFault(t)
	q := queue.New(f.fs, f.clock, queue.Config{})
	id, _ := f.seedOne(q, "payload")
	msg, err := q.Dequeue(ctx, "ns", time.Minute)
	requireNoErr(t, err, "Dequeue")
	claimed := f.stored("msg:" + id)

	f.fs.failNext("TxGet", 1)
	ackErr := q.Ack(ctx, "ns", msg.Receipt)
	requireKindMsg(t, ackErr, cascade.KindUnavailable,
		fmt.Sprintf("queue.Ack: removing message %q in namespace %q: %s", id, "ns", errInjected.Error()), "Ack with failed in-Tx read")
	f.requireUnchanged("msg:"+id, claimed, "after Ack with failed in-Tx read")

	requireNoErr(t, q.Ack(ctx, "ns", msg.Receipt), "retry Ack with the same receipt")
	if f.stored("msg:"+id) != nil {
		t.Fatal("retry Ack left the record behind")
	}
}
