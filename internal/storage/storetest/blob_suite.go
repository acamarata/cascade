// Purpose: RunBlobStoreTests — the provider.BlobStore conformance suite.
// SPORT: internal.storage.storetest/ADDED (P1-E02-W1-S02-T1).

package storetest

import (
	"bytes"
	"io"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// RunBlobStoreTests exercises every provider.BlobStore method, including
// the key-not-found error path, against a driver produced by newStore.
func RunBlobStoreTests(t *testing.T, newStore BlobStoreFactory) {
	t.Helper()
	t.Run("PutGetDeleteExists", func(t *testing.T) { testBlobPutGetDeleteExists(t, newStore(t)) })
	t.Run("GetNotFound", func(t *testing.T) { testBlobGetNotFound(t, newStore(t)) })
	t.Run("PutIsIdempotent", func(t *testing.T) { testBlobPutIdempotent(t, newStore(t)) })
	t.Run("DeleteAbsentIsNoop", func(t *testing.T) { testBlobDeleteAbsent(t, newStore(t)) })
}

func testBlobPutGetDeleteExists(t *testing.T, b provider.BlobStore) {
	t.Helper()
	ctx := testContext(t)
	content := []byte("hello blob")
	hash, err := b.Put(ctx, "ns", bytes.NewReader(content))
	requireNoError(t, err, "Put")
	if hash.IsZero() {
		t.Fatal("Put returned zero Hash")
	}
	exists, err := b.Exists(ctx, "ns", hash)
	requireNoError(t, err, "Exists")
	if !exists {
		t.Fatal("Exists = false after Put, want true")
	}
	rc, err := b.Get(ctx, "ns", hash)
	requireNoError(t, err, "Get")
	got, err := io.ReadAll(rc)
	requireNoError(t, err, "reading Get result")
	requireNoError(t, rc.Close(), "closing Get result")
	requireBytesEqual(t, got, content, "Get content")
	requireNoError(t, b.Delete(ctx, "ns", hash), "Delete")
	exists, err = b.Exists(ctx, "ns", hash)
	requireNoError(t, err, "Exists after Delete")
	if exists {
		t.Fatal("Exists = true after Delete, want false")
	}
}

func testBlobGetNotFound(t *testing.T, b provider.BlobStore) {
	t.Helper()
	ctx := testContext(t)
	var missing provider.Hash
	missing[0] = 0xAB // non-zero but never Put, so it cannot exist
	_, err := b.Get(ctx, "ns", missing)
	requireErrorKind(t, err, cascade.KindNotFound, "Get of missing hash")
}

func testBlobPutIdempotent(t *testing.T, b provider.BlobStore) {
	t.Helper()
	ctx := testContext(t)
	content := []byte("idempotent content")
	h1, err := b.Put(ctx, "ns", bytes.NewReader(content))
	requireNoError(t, err, "first Put")
	h2, err := b.Put(ctx, "ns", bytes.NewReader(content))
	requireNoError(t, err, "second Put of identical content")
	if h1 != h2 {
		t.Fatalf("Put of identical content: hashes differ, %x vs %x", h1, h2)
	}
}

func testBlobDeleteAbsent(t *testing.T, b provider.BlobStore) {
	t.Helper()
	ctx := testContext(t)
	var absent provider.Hash
	absent[0] = 0xCD
	requireNoError(t, b.Delete(ctx, "ns", absent), "Delete of absent hash (want idempotent nil)")
}
