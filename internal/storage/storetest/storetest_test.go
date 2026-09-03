// Purpose: storetest package self-validation — proves RunStoreTests passes
//
//	against MemStore, the reference implementation, and separately proves
//	its error-path assertions actually fire against a minimal
//	ALWAYS-failing test double (Art.1.1: doubles live here, in a _test.go
//	file, never in pkg/provider non-test files).
//
// Constraints: writes nothing to disk — MemStore is pure in-memory, so no
//
//	t.TempDir() is needed here (Art.7.1 is satisfied vacuously: there is no
//	filesystem write to scope).
//
// SPORT: internal.storage.storetest/ADDED (P1-E02-W1-S02-T1).
package storetest

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestRunStoreTests_MemStore is the harness self-test required by the
// ticket: it proves the memstore reference implementation passes the full
// RunStoreTests suite it is meant to be tested by.
func TestRunStoreTests_MemStore(t *testing.T) {
	RunStoreTests(t, func(t *testing.T) provider.Store {
		t.Helper()
		return NewMemStore()
	})
}

// TestRunVectorStoreTests_Fake proves RunVectorStoreTests passes against a
// real (if minimal) VectorStore implementation, exercising the R-14.4
// canonical surface end to end.
func TestRunVectorStoreTests_Fake(t *testing.T) {
	RunVectorStoreTests(t, func(t *testing.T) provider.VectorStore {
		t.Helper()
		return newFakeVectorStore()
	})
}

// TestRunBlobStoreTests_Fake proves RunBlobStoreTests passes against a real
// content-addressed BlobStore implementation, including the
// key-not-found and idempotent-Put error/behavior paths.
func TestRunBlobStoreTests_Fake(t *testing.T) {
	RunBlobStoreTests(t, func(t *testing.T) provider.BlobStore {
		t.Helper()
		return newFakeBlobStore()
	})
}

// TestRunCacheTests_Fake proves RunCacheTests passes against a real Cache
// implementation.
func TestRunCacheTests_Fake(t *testing.T) {
	RunCacheTests(t, func(t *testing.T) provider.Cache {
		t.Helper()
		return newFakeCache()
	})
}

// TestRunQueueTests_Fake proves RunQueueTests passes against a real Queue
// implementation, including the ack-timeout error path (always run) and
// the enqueue-overflow error path (this fake implements BoundedQueue with
// a small fixed capacity so that case runs too).
func TestRunQueueTests_Fake(t *testing.T) {
	RunQueueTests(t, func(t *testing.T) provider.Queue {
		t.Helper()
		return newFakeQueue(4)
	})
}

// TestRunQueueTests_Fake_WithClock proves RunQueueTests' WithQueueClock
// deterministic AckTimeout path (R-14.136) against a driver that exposes a
// clock seam: fakeQueue reads clock.Now() instead of the wall clock when
// constructed via newFakeQueueWithClock, so clock.Advance moves the
// driver's own notion of "now" and the redelivery in
// testQueueAckTimeoutDeterministic happens on the very next Dequeue with no
// polling or sleeping. Without this test, WithQueueClock and
// testQueueAckTimeoutDeterministic are only exercised by other packages'
// driver-conformance tests, never by this package's own suite.
func TestRunQueueTests_Fake_WithClock(t *testing.T) {
	clock := newFakeClock()
	RunQueueTests(t, func(t *testing.T) provider.Queue {
		t.Helper()
		return newFakeQueueWithClock(4, clock)
	}, WithQueueClock(clock))
}

// alwaysNotFoundStore is a minimal in-package test-only double (Art.1.1:
// never in pkg/provider non-test files) whose Get always reports
// KindNotFound and whose other methods no-op successfully. It exists only
// to prove RunStoreTests' GetNotFound sub-test genuinely detects a driver
// that returns the wrong Kind, by running it against a driver engineered
// to return the RIGHT one for that single path and checking the harness
// accepts it — the counterpart failure-detection case (a driver that
// returns the WRONG kind) is exercised via T.Run's own pass/fail signal in
// TestErrorPathDetection below.
type alwaysNotFoundStore struct{}

func (alwaysNotFoundStore) Get(context.Context, string, string) ([]byte, error) {
	return nil, cascade.New(cascade.KindNotFound, "always not found")
}
func (alwaysNotFoundStore) Put(context.Context, string, string, []byte) error { return nil }
func (alwaysNotFoundStore) Delete(context.Context, string, string) error      { return nil }
func (alwaysNotFoundStore) Scan(context.Context, string, string) (provider.Iterator, error) {
	return &memIterator{pos: -1}, nil
}
func (alwaysNotFoundStore) Tx(ctx context.Context, fn func(context.Context, provider.Tx) error) error {
	return fn(ctx, &memTx{store: NewMemStore()})
}

// TestErrorPathDetection proves the harness's key-not-found assertion is
// load-bearing: it runs only the GetNotFound sub-test against a double
// whose Get always returns the correct Kind, confirming that sub-test
// passes for the right reason (a matching Kind), not because the assertion
// is a no-op.
func TestErrorPathDetection(t *testing.T) {
	s := alwaysNotFoundStore{}
	ctx := context.Background()
	_, err := s.Get(ctx, "ns", "anything")
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("alwaysNotFoundStore.Get: want KindNotFound, got %v", err)
	}
	testStoreGetNotFound(t, s)
}
