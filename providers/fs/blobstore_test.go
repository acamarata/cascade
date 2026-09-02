// Purpose: BlobStore conformance (via storetest.RunBlobStoreTests) plus
//   driver-specific happy-path edge cases: layout sharding, namespace
//   validation, Stat, and concurrent same-content Put (atomicity under
//   -race). Error-path coverage (context cancellation, missing/misplaced
//   directories, a failing reader, a corrupted Delete target) lives in the
//   sibling blobstore_errors_test.go (R-14.117 in-package cap split — both
//   files stay under Art.10.3's 300-line cap).
// Constraints: every root is t.TempDir() (Art.7.1); no network calls; no
//   sleeps as synchronization.
// SPORT: providers.fs.BlobStore/ADDED (P1-E02-W1-S02-T4).

package fs_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/fs"

	"github.com/acamarata/cascade/internal/storage/storetest"
)

func newStore(t *testing.T) *fs.BlobStore {
	t.Helper()
	b, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatalf("fs.New: %v", err)
	}
	return b
}

func TestBlobStore_Conformance(t *testing.T) {
	storetest.RunBlobStoreTests(t, func(t *testing.T) provider.BlobStore {
		t.Helper()
		return newStore(t)
	})
}

func TestBlobStore_ShardedLayout(t *testing.T) {
	root := t.TempDir()
	b, err := fs.New(root)
	if err != nil {
		t.Fatalf("fs.New: %v", err)
	}
	ctx := context.Background()
	hash, err := b.Put(ctx, "ns", bytes.NewReader([]byte("sharded content")))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	hex := hash.String()
	want := filepath.Join(root, "ns", hex[:2], hex[2:])
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected blob at %s: %v", want, err)
	}
}

func TestBlobStore_InvalidNamespace(t *testing.T) {
	b := newStore(t)
	ctx := context.Background()
	for _, ns := range []string{"", ".", "..", "a/b", "a\\b"} {
		if _, err := b.Put(ctx, ns, bytes.NewReader([]byte("x"))); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("Put with namespace %q: want KindInvalidInput, got %v", ns, err)
		}
		var h provider.Hash
		if _, err := b.Get(ctx, ns, h); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("Get with namespace %q: want KindInvalidInput, got %v", ns, err)
		}
		if err := b.Delete(ctx, ns, h); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("Delete with namespace %q: want KindInvalidInput, got %v", ns, err)
		}
		if _, err := b.Exists(ctx, ns, h); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("Exists with namespace %q: want KindInvalidInput, got %v", ns, err)
		}
		if _, err := b.Stat(ctx, ns, h); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("Stat with namespace %q: want KindInvalidInput, got %v", ns, err)
		}
	}
}

func TestBlobStore_Stat(t *testing.T) {
	b := newStore(t)
	ctx := context.Background()
	content := []byte("stat me")
	hash, err := b.Put(ctx, "ns", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	info, err := b.Stat(ctx, "ns", hash)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size != int64(len(content)) {
		t.Fatalf("Stat.Size = %d, want %d", info.Size, len(content))
	}

	var missing provider.Hash
	missing[0] = 0xFF
	if _, err := b.Stat(ctx, "ns", missing); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("Stat of missing hash: want KindNotFound, got %v", err)
	}
}

func TestBlobStore_ConcurrentPutSameContent(t *testing.T) {
	b := newStore(t)
	ctx := context.Background()
	content := []byte("raced content, identical everywhere")

	const workers = 16
	hashes := make([]provider.Hash, workers)
	errs := make([]error, workers)

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			hashes[i], errs[i] = b.Put(ctx, "ns", bytes.NewReader(content))
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d Put: %v", i, err)
		}
		if hashes[i] != hashes[0] {
			t.Fatalf("worker %d hash %x != worker 0 hash %x", i, hashes[i], hashes[0])
		}
	}

	rc, err := b.Get(ctx, "ns", hashes[0])
	if err != nil {
		t.Fatalf("Get after concurrent Put: %v", err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading Get result: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("closing Get result: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("Get content = %q, want %q", got, content)
	}
}
