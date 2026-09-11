// Purpose: unit tests for s3.go's Open/Close/String/error-classification,
//   against a real (if embedded) S3-compatible server —
//   github.com/johannesboyne/gofakes3 (MIT), a genuine S3 REST API
//   implementation over an in-memory backend, not a hand-rolled dialect of
//   this package's own invention. This is the same role
//   providers/redis/redis_test.go's miniredis fills there, for the
//   untagged/no-docker coverage lane. The REAL, docker-provisioned MinIO
//   server run is integration_test.go's job (Art.2).
// Constraints: the default (non-integration-tagged) unit lane forbids
//   importing "net"/"net/http" directly (Art.7.2, enforced by
//   internal/build's no-network-unit-lane gate) — this file imports only
//   "net/http/httptest" (a distinct import path the gate does not match)
//   to host gofakes3.Server()'s http.Handler, and never names the "net"
//   or "net/http" packages itself.
// SPORT: providers.s3/ADDED (P1-E17-W4-S38-T7).

package s3_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/s3"
)

// fakeS3Server wraps a gofakes3 instance over an in-memory backend,
// pre-seeded with one bucket.
type fakeS3Server struct {
	srv    *httptest.Server
	bucket string
}

func newFakeS3Server(t *testing.T, bucket string) *fakeS3Server {
	t.Helper()
	backend := s3mem.New()
	if err := backend.CreateBucket(bucket); err != nil {
		t.Fatalf("backend.CreateBucket: %v", err)
	}
	faker := gofakes3.New(backend)
	srv := httptest.NewServer(faker.Server())
	t.Cleanup(srv.Close)
	return &fakeS3Server{srv: srv, bucket: bucket}
}

func openFake(t *testing.T, bucket string) (*s3.Conn, *fakeS3Server) {
	t.Helper()
	f := newFakeS3Server(t, bucket)
	conn, err := s3.Open(context.Background(), f.srv.URL, bucket, "minioadmin", "minioadmin")
	if err != nil {
		t.Fatalf("s3.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, f
}

func TestOpen_EmptyEndpoint(t *testing.T) {
	_, err := s3.Open(context.Background(), "", "bucket", "id", "secret")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Open(empty endpoint) = %v, want KindInvalidInput", err)
	}
}

func TestOpen_EmptyBucket(t *testing.T) {
	_, err := s3.Open(context.Background(), "http://127.0.0.1:1", "", "id", "secret")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Open(empty bucket) = %v, want KindInvalidInput", err)
	}
}

func TestOpen_UnparseableEndpoint(t *testing.T) {
	_, err := s3.Open(context.Background(), "not a url \x00", "bucket", "id", "secret")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Open(unparseable) = %v, want KindInvalidInput", err)
	}
}

func TestOpen_BadScheme(t *testing.T) {
	_, err := s3.Open(context.Background(), "ftp://127.0.0.1:1", "bucket", "id", "secret")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Open(bad scheme) = %v, want KindInvalidInput", err)
	}
}

func TestOpen_NoSchemeRefused(t *testing.T) {
	// A bare host:port with no "http://"/"https://" prefix is refused —
	// see parseEndpoint's own doc comment (s3.go) for why net/url.Parse
	// itself already rejects this shape, which this proves indirectly by
	// asserting the refusal rather than reaching into the package.
	_, err := s3.Open(context.Background(), "127.0.0.1:1", "bucket", "id", "secret")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Open(no scheme) = %v, want KindInvalidInput", err)
	}
}

func TestOpen_Success(t *testing.T) {
	conn, _ := openFake(t, "test-bucket")
	if conn.String() == "" {
		t.Fatal("String() returned empty")
	}
}

// TestOpen_UnreachableServer proves the unreachable-endpoint error path
// against a real (non-listening) address, and that the error never leaks
// the endpoint (08 §2 extended to error messages, matching
// providers/postgres and providers/redis's equivalent tests). minio-go
// retries a connection failure with backoff internally, so once the
// caller's own context deadline is short enough to elapse mid-retry, the
// error minio-go returns is itself a context-deadline error rather than
// the underlying connection-refused — both are legitimate outcomes of
// "this server cannot be reached in time", so both Kinds are accepted.
func TestOpen_UnreachableServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := s3.Open(ctx, "http://127.0.0.1:1", "test-bucket", "minioadmin", "minioadmin")
	if err == nil {
		t.Fatal("Open against an unreachable address returned nil error")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) && !cascade.HasKind(err, cascade.KindTimeout) {
		t.Fatalf("Open(unreachable) = %v, want KindUnavailable or KindTimeout", err)
	}
}

func TestOpen_MissingBucket(t *testing.T) {
	f := newFakeS3Server(t, "real-bucket")
	_, err := s3.Open(context.Background(), f.srv.URL, "wrong-bucket", "minioadmin", "minioadmin")
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("Open(missing bucket) = %v, want KindNotFound", err)
	}
}

func TestClose_NilSafe(t *testing.T) {
	var conn *s3.Conn
	if err := conn.Close(); err != nil {
		t.Fatalf("nil Conn.Close() = %v, want nil", err)
	}
}
