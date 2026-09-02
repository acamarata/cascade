// Purpose: portable conformance suite for pkg/provider's five storage
//   family interfaces. Any driver that passes its family's Run*Tests
//   function is correct by construction against the interface contract
//   (P1-E02-W1-S02-T1).
// Inputs: a factory function that constructs a fresh, empty driver instance
//   for each Run*Tests call — the suite never assumes a shared or
//   pre-seeded backing store.
// Outputs: t.Fatal/t.Error/t.Run failures reported through the supplied
//   *testing.T, in the standard Go test idiom.
// Constraints: this package is a test-helper LIBRARY (imported by driver
//   _test.go files, e.g. S-03.T4's localvector tests), so it is NOT itself
//   suffixed _test.go; it may still import "testing" and "context", the
//   idiomatic shape for exported Go test helpers (cf. net/http/httptest).
//   The suite is split across sibling files by family (Art.10.3's 300-line
//   cap authorizes splits in-package per R-14.117): store_suite.go,
//   vector_suite.go, blob_suite.go, cache_suite.go, queue_suite.go. This
//   file holds only the shared factory types and cross-family helpers.
// SPORT: internal.storage.storetest/ADDED (P1-E02-W1-S02-T1).

package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// StoreFactory constructs a fresh, empty provider.Store for one test. The
// suite calls it once per Run and once more per sub-test that needs
// isolation from prior sub-tests' writes.
type StoreFactory func(t *testing.T) provider.Store

// VectorStoreFactory constructs a fresh, empty provider.VectorStore for one
// test.
type VectorStoreFactory func(t *testing.T) provider.VectorStore

// BlobStoreFactory constructs a fresh, empty provider.BlobStore for one
// test.
type BlobStoreFactory func(t *testing.T) provider.BlobStore

// CacheFactory constructs a fresh, empty provider.Cache for one test.
type CacheFactory func(t *testing.T) provider.Cache

// QueueFactory constructs a fresh, empty provider.Queue for one test.
type QueueFactory func(t *testing.T) provider.Queue

// testContext returns a background context bounded by a generous per-case
// deadline, so a driver that deadlocks fails the test instead of hanging
// the suite forever.
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// drainIterator collects every key/value pair from it and closes it,
// failing the test if either the iteration or the close reports an error.
func drainIterator(t *testing.T, it provider.Iterator) map[string][]byte {
	t.Helper()
	out := make(map[string][]byte)
	ctx := testContext(t)
	for it.Next(ctx) {
		out[it.Key()] = append([]byte(nil), it.Value()...)
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterator error: %v", err)
	}
	if err := it.Close(); err != nil {
		t.Fatalf("iterator close error: %v", err)
	}
	return out
}

// requireNoError fails the test with msg and err's detail if err is
// non-nil. Every suite function routes its "this call must succeed"
// assertions through this one helper so that assertion logic lives in one
// place rather than being repeated, uncovered on the happy path, at every
// call site.
func requireNoError(t *testing.T, err error, msg string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", msg, err)
	}
}

// requireErrorKind fails the test unless err carries the given taxonomy
// Kind (per cascade.HasKind), reporting msg and the actual error on
// failure.
func requireErrorKind(t *testing.T, err error, kind cascade.Kind, msg string) {
	t.Helper()
	if !cascade.HasKind(err, kind) {
		t.Fatalf("%s: want Kind %s, got %v", msg, kind, err)
	}
}

// requireBytesEqual fails the test unless got and want hold identical
// bytes, reporting msg on failure.
func requireBytesEqual(t *testing.T, got, want []byte, msg string) {
	t.Helper()
	if string(got) != string(want) {
		t.Fatalf("%s: got %q, want %q", msg, got, want)
	}
}
