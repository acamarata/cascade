// Purpose: the fault-matrix subtests fault_test.go had no room left for
//   under the 300-line file cap (Get, receipt generation), plus the
//   standalone TestP1QueueAckRetryAfterDeleteFailure the suite names as
//   its own top-level -list entry (distinct from
//   fault_test.go's "AckDeleteFailureDoesNotStrandBody" subtest, which
//   proves the same seam but is not itself independently selectable by
//   name). Shares fault_test.go's faultStore/faultTx and errInjected.
// SPORT: internal.storage.queue.Queue/CHANGED.

package queue_test

import (
	"context"
	cryptorand "crypto/rand"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/queue"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// failingReader is an io.Reader that always fails, used to fault-inject
// generateReceipt's entropy source via queue.SetReceiptEntropyForTest
// (export_test.go) — a real (if rare) failure mode: an exhausted or broken
// entropy source, not a fabricated stand-in for a security check (never a stand-in).
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errInjected }

// testReceiptGenerationFailure: a failed generateReceipt call must return
// before any Store.Tx opens, leaving the record untouched, and a retry
// once the entropy source recovers must claim it normally.
func testReceiptGenerationFailure(t *testing.T) {
	ctx := context.Background()
	f := newSQLiteFault(t)
	q := queue.New(f.fs, f.clock, queue.Config{})
	id, before := f.seedOne(q, "payload")

	queue.SetReceiptEntropyForTest(t, failingReader{})
	msg, err := q.Dequeue(ctx, "ns", time.Minute)
	if err == nil || msg != nil {
		t.Fatalf("Dequeue with injected receipt-generation failure = %+v, %v, want nil, error", msg, err)
	}
	f.requireUnchanged("msg:"+id, before, "after failed receipt generation (no Tx should ever have opened)")

	queue.SetReceiptEntropyForTest(t, cryptorand.Reader)
	retry, err := q.Dequeue(ctx, "ns", time.Minute)
	requireNoErr(t, err, "retry Dequeue after receipt-generation fault cleared")
	if retry == nil || retry.ID != id {
		t.Fatalf("retry Dequeue after failed receipt generation = %+v, want redelivery of %q", retry, id)
	}
	requireNoErr(t, q.Nack(ctx, "ns", retry.Receipt), "Nack to restore the ready state")

	queue.SetReceiptEntropyForTest(t, failingReader{})
	if _, err := q.Dequeue(ctx, "ns", time.Minute); err == nil {
		t.Fatal("second injected receipt failure = nil error")
	}
	queue.SetReceiptEntropyForTest(t, cryptorand.Reader)
	f.requireDeliveredAfterReopen(queue.Config{}, id, "receipt generation failure")
}

// raceBarrierStore forces two racing Scan calls (recoverNamespace's own
// read) to both complete BEFORE either caller is allowed to proceed to its
// migration CAS — without this, two goroutines started back-to-back may
// simply run to completion one after the other on an idle scheduler
// (observed: claimed=1 faulted=0, no race at all), since MemStore's whole
// Tx/Scan path is fast, in-memory, uncontended. With the barrier, both
// goroutines are guaranteed to have scanned the SAME pre-migration bytes
// before either attempts its CompareAndSwap, so exactly one loses.
type raceBarrierStore struct {
	provider.Store
	mu      sync.Mutex
	arrived int
	release chan struct{}
}

func newRaceBarrierStore(inner provider.Store) *raceBarrierStore {
	return &raceBarrierStore{Store: inner, release: make(chan struct{})}
}

func (s *raceBarrierStore) Scan(ctx context.Context, ns, prefix string) (provider.Iterator, error) {
	it, err := s.Store.Scan(ctx, ns, prefix) // real snapshot taken first, before either party blocks
	s.mu.Lock()
	s.arrived++
	if s.arrived == 2 {
		close(s.release)
	}
	s.mu.Unlock()
	<-s.release
	return it, err
}

// testRecoveryGetFailureDuringMigrationRace: persistence.go's
// afterMigrationTx re-reads via a plain store.Get (never a tx.Get) only
// when its own migration CAS lost a race — the "Get" fault seam,
// distinct from claimLocked's CAS ("attempt Put") and recoverNamespace's
// Scan. With no other store.Get call site in production code and
// raceBarrierStore forcing a genuine race, exactly one of the two
// instances loses the migration CAS and hits the injected failure.
func testRecoveryGetFailureDuringMigrationRace(t *testing.T) {
	ctx := context.Background()
	clock := runtime.NewFixedClock(time.Unix(1_700_000_000, 0))
	inner := storetest.NewMemStore()
	legacyID := "00000000000000000001-ddddddd1"
	legacy := make([]byte, 4+len("get-race-payload"))
	copy(legacy[4:], "get-race-payload")
	requireNoErr(t, inner.Put(ctx, "ns", "msg:"+legacyID, legacy), "seed legacy record")

	barrier := newRaceBarrierStore(inner)
	fs := newFaultStore(barrier)
	fs.failNext("Get", 1)
	q1 := queue.New(fs, clock, queue.Config{})
	q2 := queue.New(fs, clock, queue.Config{})

	type result struct {
		msg *provider.Message
		err error
	}
	results := make(chan result, 2)
	go func() { m, e := q1.Dequeue(ctx, "ns", time.Minute); results <- result{m, e} }()
	go func() { m, e := q2.Dequeue(ctx, "ns", time.Minute); results <- result{m, e} }()

	var claimed, faulted int
	for i := 0; i < 2; i++ {
		r := <-results
		switch {
		case r.err != nil:
			faulted++
		case r.msg != nil:
			claimed++
		}
	}
	if faulted != 1 || claimed != 1 {
		t.Fatalf("concurrent migrated Dequeue racing an injected Get failure: claimed=%d faulted=%d, want exactly 1 and 1 (the losing migration CAS must hit the fault, the winner must still claim)", claimed, faulted)
	}

	retry, err := queue.New(fs, clock, queue.Config{}).Dequeue(ctx, "ns", time.Minute)
	requireNoErr(t, err, "retry Dequeue after migration-race Get fault")
	if retry != nil {
		t.Fatalf("retry Dequeue = %+v, want nil — the record was already claimed by the race winner, not stranded by the loser's fault", retry)
	}

	requireMigratedRecordSurvivesReopen(t, inner, clock, legacyID)
}

// testRecoveryScanFailure: the audit's finding, adapted to the
// namespace-recovery Scan (claim/Ack/Nack never re-Get after recovery).
// Moved here from fault_test.go to stay under the 300-line file cap.
func testRecoveryScanFailure(t *testing.T) {
	ctx := context.Background()
	clock := runtime.NewFixedClock(time.Unix(1_700_000_000, 0))
	inner := storetest.NewMemStore()
	seed := queue.New(inner, clock, queue.Config{})
	id, err := seed.Enqueue(ctx, "ns", []byte("payload"))
	requireNoErr(t, err, "seed Enqueue")

	fs := newFaultStore(inner)
	fs.failNext("Scan", 1)
	q := queue.New(fs, clock, queue.Config{})
	if _, dequeueErr := q.Dequeue(ctx, "ns", time.Minute); dequeueErr == nil {
		t.Fatal("Dequeue triggering a failed recovery Scan = nil error, want an error")
	}

	q2 := queue.New(fs, clock, queue.Config{})
	retry, err := q2.Dequeue(ctx, "ns", time.Minute)
	requireNoErr(t, err, "retry Dequeue after Scan fault cleared")
	if retry == nil || retry.ID != id {
		t.Fatalf("retry Dequeue after failed recovery scan = %+v, want %q", retry, id)
	}
}

// TestP1QueueAckRetryAfterDeleteFailure proves the Ack-retry contract: after an
// injected Ack Delete failure, retrying Ack with the same receipt reaches a
// durable completed outcome — succeeding once the fault clears — and NEVER
// reports a stale-receipt error (KindTimeout) while the body is still
// stored. That exact mis-mapping was the audit's stranding bug (ack.go's
// own doc comment: releasing tracking before the delete actually commits
// makes a retried receipt look falsely unrecognized).
func TestP1QueueAckRetryAfterDeleteFailure(t *testing.T) {
	ctx := context.Background()
	clock := runtime.NewFixedClock(time.Unix(1_700_000_000, 0))
	fs := newFaultStore(storetest.NewMemStore())
	q := queue.New(fs, clock, queue.Config{})
	id, err := q.Enqueue(ctx, "ns", []byte("payload"))
	requireNoErr(t, err, "Enqueue")
	msg, err := q.Dequeue(ctx, "ns", time.Minute)
	requireNoErr(t, err, "Dequeue")
	if msg == nil || msg.ID != id {
		t.Fatalf("Dequeue = %+v, want id %q", msg, id)
	}

	fs.failNext("TxDelete", 1)
	firstErr := q.Ack(ctx, "ns", msg.Receipt)
	if firstErr == nil {
		t.Fatal("Ack with injected Delete failure = nil, want an error")
	}
	if cascade.HasKind(firstErr, cascade.KindTimeout) {
		t.Fatalf("Ack with injected Delete failure reported a stale receipt while the body is still stored: %v", firstErr)
	}
	if stillStored, getErr := fs.Get(ctx, "ns", "msg:"+id); getErr != nil || len(stillStored) == 0 {
		t.Fatalf("body missing after a failed Ack Delete (err=%v) — it must still be stored", getErr)
	}

	retryErr := q.Ack(ctx, "ns", msg.Receipt)
	if cascade.HasKind(retryErr, cascade.KindTimeout) {
		t.Fatalf("retry Ack with the same receipt reported KindTimeout (stale) while the body may still be stored: %v", retryErr)
	}
	if retryErr != nil {
		t.Fatalf("retry Ack with the same receipt after fault cleared = %v, want success (fault was armed for exactly one call)", retryErr)
	}
	if _, getErr := fs.Get(ctx, "ns", "msg:"+id); !cascade.HasKind(getErr, cascade.KindNotFound) {
		t.Fatalf("body still present after a successful retried Ack: err=%v", getErr)
	}
}

// requireMigratedRecordSurvivesReopen: a fresh Queue over the same store
// must leave the winner's unexpired claim alone (byte-identical, not
// redelivered) and redeliver it once the deadline has passed.
func requireMigratedRecordSurvivesReopen(t *testing.T, inner provider.Store, clock *runtime.FixedClock, legacyID string) {
	t.Helper()
	ctx := context.Background()
	afterRace, err := inner.Get(ctx, "ns", "msg:"+legacyID)
	requireNoErr(t, err, "Get after the migration race")
	reopened := queue.New(inner, clock, queue.Config{})
	if early, err := reopened.Dequeue(ctx, "ns", time.Minute); err != nil || early != nil {
		t.Fatalf("reopened Dequeue before the winner's deadline = %+v, %v, want nil", early, err)
	}
	if again, _ := inner.Get(ctx, "ns", "msg:"+legacyID); string(again) != string(afterRace) {
		t.Fatalf("recovery rewrote an already-migrated record: before %x, after %x", afterRace, again)
	}
	clock.Advance(2 * time.Minute)
	late, err := reopened.Dequeue(ctx, "ns", time.Minute)
	requireNoErr(t, err, "reopened Dequeue after the deadline")
	if late == nil || late.ID != legacyID {
		t.Fatalf("reopened Dequeue after the deadline = %+v, want %q", late, legacyID)
	}
}
