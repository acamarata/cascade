//go:build integration

// Purpose: the S3 storetest-under-docker lane's real conformance run —
//
//	RunBlobStoreTests against a REAL S3-compatible server (MinIO), never
//	a self-authored S3 dialect or hand-rolled fake (Art.2). s3_test.go's
//	fakeS3Server-backed run gives the untagged, no-docker coverage lane;
//	this file is the docker-tagged proof the ticket's own checks name.
//
// Inputs: CASCADE_TEST_S3_ENDPOINT, CASCADE_TEST_S3_BUCKET,
//
//	CASCADE_TEST_S3_ACCESS_KEY, CASCADE_TEST_S3_SECRET_KEY — a real
//	reachable S3-compatible server and a bucket that already exists on
//	it (see testdata/README.md for the exact docker invocation this
//	lane expects).
//
// Constraints: go:build integration only — no "postgres" or "s3" tag,
//
//	matching the ticket's own check commands
//	(`go test ./providers/s3/ -tags=integration -run ...`) and
//	providers/redis/integration_test.go's precedent, since this package
//	carries no build-time gate of its own.
//
// SPORT: providers.s3/ADDED (P1-E17-W4-S38-T7).
package s3_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/s3"
)

// realS3Config reads the four env-refs this lane needs, skipping the test
// if any is unset.
type realS3Config struct {
	endpoint, bucket, accessKey, secretKey string
}

func realS3(t *testing.T) realS3Config {
	t.Helper()
	cfg := realS3Config{
		endpoint:  os.Getenv("CASCADE_TEST_S3_ENDPOINT"),
		bucket:    os.Getenv("CASCADE_TEST_S3_BUCKET"),
		accessKey: os.Getenv("CASCADE_TEST_S3_ACCESS_KEY"),
		secretKey: os.Getenv("CASCADE_TEST_S3_SECRET_KEY"),
	}
	if cfg.endpoint == "" || cfg.bucket == "" || cfg.accessKey == "" || cfg.secretKey == "" {
		t.Skip("CASCADE_TEST_S3_ENDPOINT/_BUCKET/_ACCESS_KEY/_SECRET_KEY not all set — this lane requires a real reachable S3-compatible server")
	}
	return cfg
}

// TestS3BlobStoretestUnderDocker is the ticket's named CI entry point
// against a REAL S3-compatible server.
func TestS3BlobStoretestUnderDocker(t *testing.T) {
	cfg := realS3(t)
	storetest.RunBlobStoreTests(t, func(t *testing.T) provider.BlobStore {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := s3.Open(ctx, cfg.endpoint, cfg.bucket, cfg.accessKey, cfg.secretKey)
		if err != nil {
			t.Fatalf("s3.Open: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return s3.NewBlobStore(conn)
	})
}

// TestOpen_UnreachableServer_Real proves the unreachable-endpoint error
// path against a real (non-listening) address, distinct from
// s3_test.go's fake-backed version, on this lane.
func TestOpen_UnreachableServer_Real(t *testing.T) {
	cfg := realS3(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := s3.Open(ctx, "http://127.0.0.1:1", cfg.bucket, cfg.accessKey, cfg.secretKey)
	if !cascade.HasKind(err, cascade.KindUnavailable) && !cascade.HasKind(err, cascade.KindTimeout) {
		t.Fatalf("Open(unreachable) = %v, want KindUnavailable or KindTimeout", err)
	}
}

// TestOpen_MissingBucket_Real proves the missing-bucket error path
// against the real server, using a bucket name that should not exist.
func TestOpen_MissingBucket_Real(t *testing.T) {
	cfg := realS3(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := s3.Open(ctx, cfg.endpoint, "cascade-test-bucket-does-not-exist", cfg.accessKey, cfg.secretKey)
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("Open(missing bucket) = %v, want KindNotFound", err)
	}
}
