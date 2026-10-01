// Purpose: fault-injected internal-Store proofs (an earlier audit, adapted to this
//   driver's Store.Tx seams) — claim CAS, Ack Delete, both DLQ-move
//   halves, and a recovery Scan failure must leave the record recoverable,
//   never stranded or falsely gone. faultStore/faultTx wrap a real
//   MemStore, returning a REAL error at a named seam for N calls.
// SPORT: internal.storage.queue.Queue/CHANGED.

package queue_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/queue"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

var errInjected = cascade.Newf(cascade.KindUnavailable, "queue: injected fault")

// faultStore wraps a real Store, armed (failNext) to return errInjected
// from a named op for exactly its next N calls, then delegate to inner.
type faultStore struct {
	inner provider.Store
	mu    sync.Mutex
	fails map[string]int
}

func newFaultStore(inner provider.Store) *faultStore {
	return &faultStore{inner: inner, fails: make(map[string]int)}
}
func (f *faultStore) failNext(op string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fails[op] = n
}
func (f *faultStore) shouldFail(op string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fails[op] > 0 {
		f.fails[op]--
		return true
	}
	return false
}

func (f *faultStore) Get(ctx context.Context, ns, key string) ([]byte, error) {
	if f.shouldFail("Get") {
		return nil, errInjected
	}
	return f.inner.Get(ctx, ns, key)
}

// Put and Delete pass straight through: production Dequeue/Ack/Nack/DLQ
// code (queue.go, ack.go) only ever writes through Store.Tx, never these
// direct methods, so there is no seam here worth faulting.
func (f *faultStore) Put(ctx context.Context, ns, key string, v []byte) error {
	return f.inner.Put(ctx, ns, key, v)
}
func (f *faultStore) Delete(ctx context.Context, ns, key string) error {
	return f.inner.Delete(ctx, ns, key)
}

func (f *faultStore) Scan(ctx context.Context, ns, prefix string) (provider.Iterator, error) {
	if f.shouldFail("Scan") {
		return nil, errInjected
	}
	return f.inner.Scan(ctx, ns, prefix)
}

func (f *faultStore) Tx(ctx context.Context, fn func(context.Context, provider.Tx) error) error {
	return f.inner.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		return fn(ctx, &faultTx{inner: tx, store: f})
	})
}

// faultTx wraps the provider.Tx a Store.Tx call hands its closure, so a
// test can arm a failure INSIDE a transaction (e.g. "TxDelete").
type faultTx struct {
	inner provider.Tx
	store *faultStore
}

func (t *faultTx) Get(ctx context.Context, ns, key string) ([]byte, error) {
	if t.store.shouldFail("TxGet") {
		return nil, errInjected
	}
	return t.inner.Get(ctx, ns, key)
}

// Put passes straight through: no production code path (queue.go, ack.go)
// ever calls tx.Put — only tx.Get, tx.Delete and tx.CompareAndSwap.
func (t *faultTx) Put(ctx context.Context, ns, key string, v []byte) error {
	return t.inner.Put(ctx, ns, key, v)
}

func (t *faultTx) Delete(ctx context.Context, ns, key string) error {
	if t.store.shouldFail("TxDelete") {
		return errInjected
	}
	return t.inner.Delete(ctx, ns, key)
}

func (t *faultTx) CompareAndSwap(ctx context.Context, ns, key string, old, newValue []byte) error {
	if t.store.shouldFail("TxCAS") {
		return errInjected
	}
	return t.inner.CompareAndSwap(ctx, ns, key, old, newValue)
}

// TestP1QueueStorageFaults: one subtest per named fault seam (Get, attempt
// Put, receipt generation, Ack Delete, DLQ record write, DLQ live-record
// removal), plus the pre-existing recovery-Scan case as bonus coverage.
func TestP1QueueStorageFaults(t *testing.T) {
	t.Run("RecoveryGetFailureDuringMigrationRace", testRecoveryGetFailureDuringMigrationRace)
	t.Run("ClaimReceiptGenerationFailureDoesNotStrandMessage", testReceiptGenerationFailure)
	t.Run("ClaimCASFailureDoesNotStrandMessage", testClaimCASFailure)
	t.Run("AckDeleteFailureDoesNotStrandBody", testAckDeleteFailure)
	t.Run("DLQCreateFailureLeavesLiveRecordIntact", testDLQCreateFailure)
	t.Run("DLQLiveDeleteFailureRollsBackBothHalves", testDLQLiveDeleteFailure)
	t.Run("RecoveryScanFailureDoesNotReturnEmptyQueue", testRecoveryScanFailure)
}

// TestQueue_MigrationIdempotentUnderConcurrentInstances: two instances
// race migrating the SAME legacy record; exactly one claim must result.
func TestQueue_MigrationIdempotentUnderConcurrentInstances(t *testing.T) {
	ctx := context.Background()
	clock := runtime.NewFixedClock(time.Unix(1_700_000_000, 0))
	store := storetest.NewMemStore()
	legacyID := "00000000000000000001-bbbbbbbb"
	legacy := make([]byte, 4+len("racing-payload"))
	copy(legacy[4:], "racing-payload")
	requireNoErr(t, store.Put(ctx, "ns", "msg:"+legacyID, legacy), "seed legacy record")

	q1 := queue.New(store, clock, queue.Config{})
	q2 := queue.New(store, clock, queue.Config{})
	type result struct {
		msg *provider.Message
		err error
	}
	results := make(chan result, 2)
	go func() { m, e := q1.Dequeue(ctx, "ns", time.Minute); results <- result{m, e} }()
	go func() { m, e := q2.Dequeue(ctx, "ns", time.Minute); results <- result{m, e} }()

	var claims []*provider.Message
	for i := 0; i < 2; i++ {
		r := <-results
		requireNoErr(t, r.err, "concurrent migrated Dequeue")
		if r.msg != nil {
			claims = append(claims, r.msg)
		}
	}
	if len(claims) != 1 || claims[0].ID != legacyID {
		t.Fatalf("concurrent migration = %+v, want exactly one claim of %q", claims, legacyID)
	}
}
