// Purpose: RunStoreTests — the provider.Store conformance suite.
// SPORT: internal.storage.storetest/ADDED (P1-E02-W1-S02-T1).

package storetest

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// RunStoreTests exercises every provider.Store method, including the
// key-not-found and tx-conflict error paths, against a driver produced by
// newStore. A driver that passes RunStoreTests is correct by construction
// against the provider.Store contract.
func RunStoreTests(t *testing.T, newStore StoreFactory) {
	t.Helper()
	t.Run("PutGetDelete", func(t *testing.T) { testStorePutGetDelete(t, newStore(t)) })
	t.Run("GetNotFound", func(t *testing.T) { testStoreGetNotFound(t, newStore(t)) })
	t.Run("DeleteAbsentIsNoop", func(t *testing.T) { testStoreDeleteAbsent(t, newStore(t)) })
	t.Run("NamespaceIsolation", func(t *testing.T) { testStoreNamespaceIsolation(t, newStore(t)) })
	t.Run("Scan", func(t *testing.T) { testStoreScan(t, newStore(t)) })
	t.Run("TxCommits", func(t *testing.T) { testStoreTxCommits(t, newStore(t)) })
	t.Run("TxRollsBackOnError", func(t *testing.T) { testStoreTxRollback(t, newStore(t)) })
	t.Run("TxCompareAndSwap", func(t *testing.T) { testStoreCAS(t, newStore(t)) })
	t.Run("TxCompareAndSwapConflict", func(t *testing.T) { testStoreCASConflict(t, newStore(t)) })
}

func testStorePutGetDelete(t *testing.T, s provider.Store) {
	t.Helper()
	ctx := testContext(t)
	requireNoError(t, s.Put(ctx, "ns", "k1", []byte("v1")), "Put")
	got, err := s.Get(ctx, "ns", "k1")
	requireNoError(t, err, "Get")
	requireBytesEqual(t, got, []byte("v1"), "Get")
	requireNoError(t, s.Delete(ctx, "ns", "k1"), "Delete")
	_, err = s.Get(ctx, "ns", "k1")
	requireErrorKind(t, err, cascade.KindNotFound, "Get after Delete")
}

func testStoreGetNotFound(t *testing.T, s provider.Store) {
	t.Helper()
	ctx := testContext(t)
	_, err := s.Get(ctx, "ns", "missing")
	requireErrorKind(t, err, cascade.KindNotFound, "Get of missing key")
	if !errors.Is(err, cascade.ErrNotFound) {
		t.Fatalf("Get of missing key: want errors.Is(err, cascade.ErrNotFound), got %v", err)
	}
}

func testStoreDeleteAbsent(t *testing.T, s provider.Store) {
	t.Helper()
	ctx := testContext(t)
	requireNoError(t, s.Delete(ctx, "ns", "never-existed"), "Delete of absent key (want idempotent nil)")
}

func testStoreNamespaceIsolation(t *testing.T, s provider.Store) {
	t.Helper()
	ctx := testContext(t)
	requireNoError(t, s.Put(ctx, "a", "k", []byte("in-a")), "Put ns a")
	_, err := s.Get(ctx, "b", "k")
	requireErrorKind(t, err, cascade.KindNotFound, "Get same key in ns b")
}

func testStoreScan(t *testing.T, s provider.Store) {
	t.Helper()
	ctx := testContext(t)
	want := map[string][]byte{
		"prefix/1": []byte("a"),
		"prefix/2": []byte("b"),
	}
	for k, v := range want {
		requireNoError(t, s.Put(ctx, "ns", k, v), "Put "+k)
	}
	requireNoError(t, s.Put(ctx, "ns", "other/1", []byte("c")), "Put other/1")
	it, err := s.Scan(ctx, "ns", "prefix/")
	requireNoError(t, err, "Scan")
	got := drainIterator(t, it)
	if len(got) != len(want) {
		t.Fatalf("Scan returned %d entries, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		requireBytesEqual(t, got[k], v, "Scan["+k+"]")
	}
}

func testStoreTxCommits(t *testing.T, s provider.Store) {
	t.Helper()
	ctx := testContext(t)
	err := s.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		return tx.Put(ctx, "ns", "tx-key", []byte("tx-value"))
	})
	requireNoError(t, err, "Tx")
	got, err := s.Get(ctx, "ns", "tx-key")
	requireNoError(t, err, "Get after committed Tx")
	requireBytesEqual(t, got, []byte("tx-value"), "Get after committed Tx")
}

func testStoreTxRollback(t *testing.T, s provider.Store) {
	t.Helper()
	ctx := testContext(t)
	sentinel := errors.New("rollback sentinel")
	err := s.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		if err := tx.Put(ctx, "ns", "rolled-back", []byte("never-visible")); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Tx error = %v, want errors.Is(err, sentinel)", err)
	}
	_, err = s.Get(ctx, "ns", "rolled-back")
	requireErrorKind(t, err, cascade.KindNotFound, "Get after rolled-back Tx")
}

func testStoreCAS(t *testing.T, s provider.Store) {
	t.Helper()
	ctx := testContext(t)
	err := s.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		return tx.CompareAndSwap(ctx, "ns", "cas-key", nil, []byte("created"))
	})
	requireNoError(t, err, "CompareAndSwap(create)")
	err = s.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		return tx.CompareAndSwap(ctx, "ns", "cas-key", []byte("created"), []byte("updated"))
	})
	requireNoError(t, err, "CompareAndSwap(update)")
	got, err := s.Get(ctx, "ns", "cas-key")
	requireNoError(t, err, "Get after CAS")
	requireBytesEqual(t, got, []byte("updated"), "Get after CAS")
}

func testStoreCASConflict(t *testing.T, s provider.Store) {
	t.Helper()
	ctx := testContext(t)
	requireNoError(t, s.Put(ctx, "ns", "cas-conflict", []byte("current")), "Put")
	err := s.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		return tx.CompareAndSwap(ctx, "ns", "cas-conflict", []byte("stale-expectation"), []byte("new"))
	})
	requireErrorKind(t, err, cascade.KindConflict, "CompareAndSwap conflict")
	got, err := s.Get(ctx, "ns", "cas-conflict")
	requireNoError(t, err, "Get after CAS conflict")
	requireBytesEqual(t, got, []byte("current"), "Get after CAS conflict (want unchanged)")
}
