// Purpose: the fault-matrix subtests of TestP1QueueStorageFaults that run
//   over a REAL reopenable sqlite file: after an injected failure at a
//   named seam the persisted record must be byte-identical to what it was
//   before the failed transition, and a Queue built over the REOPENED file
//   must still reach it (deliver it, or finish its dead-letter move) — a
//   process-local retry alone is not enough. Shares fault_test.go's
//   faultStore/faultTx and errInjected.
// SPORT: internal.storage.queue.Queue/CHANGED.

package queue_test

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/queue"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/sqlite"
)

// sqliteFault is one real sqlite file plus a faultStore armed over it.
type sqliteFault struct {
	t      *testing.T
	path   string
	driver *sqlite.Driver
	fs     *faultStore
	clock  *runtime.FixedClock
}

func newSQLiteFault(t *testing.T) *sqliteFault {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fault.db")
	d := openTestDriver(t, path)
	return &sqliteFault{t: t, path: path, driver: d, fs: newFaultStore(d), clock: runtime.NewFixedClock(time.Unix(1_700_000_000, 0))}
}

// stored returns the raw persisted bytes at key, or nil when absent.
func (f *sqliteFault) stored(key string) []byte {
	f.t.Helper()
	v, err := f.driver.Get(context.Background(), "ns", key)
	if cascade.HasKind(err, cascade.KindNotFound) {
		return nil
	}
	requireNoErr(f.t, err, "stored Get "+key)
	return v
}

// reopen closes the file and returns a brand-new Queue over a freshly
// opened driver on the same path, sharing the fixture clock.
func (f *sqliteFault) reopen(cfg queue.Config) *queue.Queue {
	f.t.Helper()
	requireNoErr(f.t, f.driver.Close(), "close before reopen")
	f.driver = openTestDriver(f.t, f.path)
	return queue.New(f.driver, f.clock, cfg)
}

// requireUnchanged fails unless key still holds exactly want.
func (f *sqliteFault) requireUnchanged(key string, want []byte, what string) {
	f.t.Helper()
	if got := f.stored(key); !bytes.Equal(got, want) {
		f.t.Fatalf("%s: record %q changed: before %x, after %x", what, key, want, got)
	}
}

// requireDeliveredAfterReopen reopens and expects id to be claimable.
func (f *sqliteFault) requireDeliveredAfterReopen(cfg queue.Config, id, what string) {
	f.t.Helper()
	q := f.reopen(cfg)
	msg, err := q.Dequeue(context.Background(), "ns", time.Minute)
	requireNoErr(f.t, err, what+": Dequeue after reopen")
	if msg == nil || msg.ID != id {
		f.t.Fatalf("%s: Dequeue after reopen = %+v, want %q", what, msg, id)
	}
}

// seedOne enqueues one message and returns its id and stored bytes.
func (f *sqliteFault) seedOne(q *queue.Queue, payload string) (string, []byte) {
	f.t.Helper()
	id, err := q.Enqueue(context.Background(), "ns", []byte(payload))
	requireNoErr(f.t, err, "seed Enqueue")
	return id, f.stored("msg:" + id)
}

// testClaimCASFailure is the "attempt Put" seam: the claim's CAS fails.
func testClaimCASFailure(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteFault(t)
	q := queue.New(f.fs, f.clock, queue.Config{})
	id, before := f.seedOne(q, "payload")

	f.fs.failNext("TxCAS", 1)
	if msg, err := q.Dequeue(ctx, "ns", time.Minute); err == nil || msg != nil {
		t.Fatalf("Dequeue with injected claim CAS failure = %+v, %v, want nil, error", msg, err)
	}
	f.requireUnchanged("msg:"+id, before, "after failed claim CAS")
	retry, err := q.Dequeue(ctx, "ns", time.Minute) // same instance: nothing stranded in memory
	requireNoErr(t, err, "retry Dequeue on the same instance")
	if retry == nil || retry.ID != id {
		t.Fatalf("retry Dequeue = %+v, want redelivery of %q", retry, id)
	}
	requireNoErr(t, q.Nack(ctx, "ns", retry.Receipt), "Nack to restore the pre-claim state")

	f.fs.failNext("TxCAS", 1)
	if _, err := q.Dequeue(ctx, "ns", time.Minute); err == nil {
		t.Fatal("second injected claim CAS failure = nil error")
	}
	f.requireDeliveredAfterReopen(queue.Config{}, id, "claim CAS failure")
}

// testAckDeleteFailure is the "Ack Delete" seam.
func testAckDeleteFailure(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteFault(t)
	q := queue.New(f.fs, f.clock, queue.Config{})
	id, _ := f.seedOne(q, "payload")
	msg, err := q.Dequeue(ctx, "ns", time.Minute)
	requireNoErr(t, err, "Dequeue")
	claimed := f.stored("msg:" + id)

	f.fs.failNext("TxDelete", 2)
	for attempt := 1; attempt <= 2; attempt++ { // the same receipt twice: never stale while the body is stored
		ackErr := q.Ack(ctx, "ns", msg.Receipt)
		if ackErr == nil || cascade.HasKind(ackErr, cascade.KindTimeout) || cascade.HasKind(ackErr, cascade.KindConflict) {
			t.Fatalf("Ack attempt %d with injected Delete failure = %v, want a non-nil error that is neither stale (Timeout) nor Conflict", attempt, ackErr)
		}
		f.requireUnchanged("msg:"+id, claimed, "after failed Ack delete")
	}

	q2 := f.reopen(queue.Config{})
	if early, err := q2.Dequeue(ctx, "ns", time.Minute); err != nil || early != nil {
		t.Fatalf("reopened Dequeue before the deadline = %+v, %v, want nil (unexpired claim)", early, err)
	}
	f.clock.Advance(2 * time.Minute)
	late, err := q2.Dequeue(ctx, "ns", time.Minute)
	requireNoErr(t, err, "reopened Dequeue after the deadline")
	if late == nil || late.ID != id {
		t.Fatalf("reopened Dequeue after the deadline = %+v, want %q", late, id)
	}
}

// dlqFixture seeds one message, spends its only attempt, and expires the
// claim, so the next Dequeue must dead-letter it.
func dlqFixture(t *testing.T) (*sqliteFault, *queue.Queue, string, []byte) {
	t.Helper()
	f := newSQLiteFault(t)
	q := queue.New(f.fs, f.clock, queue.Config{MaxAttempts: 1})
	id, _ := f.seedOne(q, "doomed")
	if msg, err := q.Dequeue(context.Background(), "ns", time.Nanosecond); err != nil || msg == nil {
		t.Fatalf("first Dequeue = %+v, %v", msg, err)
	}
	before := f.stored("msg:" + id)
	f.clock.Advance(time.Minute)
	return f, q, id, before
}

// requireDeadLetteredAfterReopen: the reopened queue finishes the move.
func requireDeadLetteredAfterReopen(t *testing.T, f *sqliteFault, id, what string) {
	t.Helper()
	q := f.reopen(queue.Config{MaxAttempts: 1})
	if msg, err := q.Dequeue(context.Background(), "ns", time.Minute); err != nil || msg != nil {
		t.Fatalf("%s: reopened Dequeue = %+v, %v, want nil (exhausted message is dead-lettered, not redelivered)", what, msg, err)
	}
	if f.stored("dlq:"+id) == nil || f.stored("msg:"+id) != nil {
		t.Fatalf("%s: after reopen want dlq:%s present and msg:%s gone", what, id, id)
	}
}

// testDLQCreateFailure is the "DLQ record write" seam.
func testDLQCreateFailure(t *testing.T) {
	f, q, id, before := dlqFixture(t)
	f.fs.failNext("TxCAS", 1) // the dlq: record's conditional create
	if _, err := q.Dequeue(context.Background(), "ns", time.Minute); err == nil {
		t.Fatal("Dequeue with a failed DLQ create = nil error, want an error")
	}
	f.requireUnchanged("msg:"+id, before, "after failed DLQ create")
	if f.stored("dlq:"+id) != nil {
		t.Fatal("dlq: record exists after a failed create")
	}
	if _, err := q.Dequeue(context.Background(), "ns", time.Minute); err != nil {
		t.Fatalf("same-instance retry after the fault cleared = %v, want the move to complete", err)
	}
	if f.stored("dlq:"+id) == nil {
		t.Fatal("same-instance retry did not dead-letter the message: it was stranded in memory")
	}
}

// testDLQLiveDeleteFailure is the "DLQ live-record removal" seam.
func testDLQLiveDeleteFailure(t *testing.T) {
	f, q, id, before := dlqFixture(t)
	f.fs.failNext("TxDelete", 1) // the live record's fenced removal
	if _, err := q.Dequeue(context.Background(), "ns", time.Minute); err == nil {
		t.Fatal("Dequeue with a failed DLQ live-delete = nil error, want an error")
	}
	if f.stored("dlq:"+id) != nil {
		t.Fatal("dlq: record exists after a failed live delete: the Tx did not roll both halves back together")
	}
	f.requireUnchanged("msg:"+id, before, "after failed DLQ live delete")
	requireDeadLetteredAfterReopen(t, f, id, "DLQ live-delete failure")
}
