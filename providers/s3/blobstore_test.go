// Purpose: the provider.BlobStore conformance run for the S3 driver
//   against fakeS3Server (s3_test.go's doc comment explains why: a real
//   S3 wire-shape double, not a hand-rolled dialect, for the untagged/
//   no-docker coverage lane). The REAL, docker-provisioned MinIO server
//   run is integration_test.go's TestS3BlobStoretestUnderDocker (Art.2).
// SPORT: providers.s3.BlobStore/ADDED (P1-E17-W4-S38-T7).

package s3_test

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/s3"
)

func newTestBlobStore(t *testing.T) provider.BlobStore {
	t.Helper()
	conn, _ := openFake(t, "test-bucket")
	return s3.NewBlobStore(conn)
}

func TestBlobStore_Conformance(t *testing.T) {
	storetest.RunBlobStoreTests(t, func(t *testing.T) provider.BlobStore {
		return newTestBlobStore(t)
	})
}

// TestBlobStore_InvalidNamespace proves every method refuses a namespace
// that fails ValidNamespace — the same rule providers/fs.ValidNamespace
// enforces for the local BlobStore driver.
func TestBlobStore_InvalidNamespace(t *testing.T) {
	b := newTestBlobStore(t)
	ctx := context.Background()
	var hash provider.Hash

	if _, err := b.Put(ctx, "a/b", nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Put(invalid namespace) = %v, want KindInvalidInput", err)
	}
	if _, err := b.Get(ctx, "..", hash); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Get(invalid namespace) = %v, want KindInvalidInput", err)
	}
	if err := b.Delete(ctx, "", hash); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Delete(invalid namespace) = %v, want KindInvalidInput", err)
	}
	if _, err := b.Exists(ctx, ".", hash); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Exists(invalid namespace) = %v, want KindInvalidInput", err)
	}
}

// TestBlobStore_ContextCanceled proves every method refuses an
// already-canceled context before making any request.
func TestBlobStore_ContextCanceled(t *testing.T) {
	b := newTestBlobStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var hash provider.Hash

	if _, err := b.Put(ctx, "ns", nil); !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("Put(canceled ctx) = %v, want KindCanceled", err)
	}
	if _, err := b.Get(ctx, "ns", hash); !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("Get(canceled ctx) = %v, want KindCanceled", err)
	}
	if err := b.Delete(ctx, "ns", hash); !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("Delete(canceled ctx) = %v, want KindCanceled", err)
	}
	if _, err := b.Exists(ctx, "ns", hash); !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("Exists(canceled ctx) = %v, want KindCanceled", err)
	}
}
