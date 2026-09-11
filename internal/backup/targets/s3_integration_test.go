//go:build integration

// Purpose: TestTargetS3RealEndpoint — the Art.2 real counterpart for
// S3Target: a real docker-provisioned MinIO container, not gofakes3
// (s3_test.go's untagged-lane double). Mirrors the S-38.T4/T7 docker-lane
// pattern (providers/s3/integration_test.go): the caller starts MinIO
// itself (`docker run ... minio/minio server /data`), creates a bucket,
// and sets CASCADE_TEST_S3_ENDPOINT/_BUCKET/_ACCESS_KEY/_SECRET_KEY; this
// test skips (never fakes a pass) when they are unset.
//
// SPORT: internal.backup.targets.s3/ADDED (P1-E19-W4-S41-T3).
package targets_test

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/backup/targets"
)

func TestTargetS3RealEndpoint(t *testing.T) {
	endpoint := os.Getenv("CASCADE_TEST_S3_ENDPOINT")
	bucket := os.Getenv("CASCADE_TEST_S3_BUCKET")
	accessKey := os.Getenv("CASCADE_TEST_S3_ACCESS_KEY")
	secretKey := os.Getenv("CASCADE_TEST_S3_SECRET_KEY")
	if endpoint == "" || bucket == "" || accessKey == "" || secretKey == "" {
		t.Skip("CASCADE_TEST_S3_ENDPOINT/_BUCKET/_ACCESS_KEY/_SECRET_KEY not set; skipping real MinIO run")
	}

	cfg := targets.S3Config{Endpoint: endpoint, Bucket: bucket, AccessKeyID: accessKey, SecretAccessKey: secretKey}
	ctx := context.Background()
	tgt, err := targets.NewS3Target(ctx, cfg, testEgressEngine(t))
	if err != nil {
		t.Fatalf("NewS3Target against real MinIO: %v", err)
	}

	want := "real minio round trip " + t.Name()
	if err := tgt.Put(ctx, "integration/roundtrip", strings.NewReader(want)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rc, err := tgt.Get(ctx, "integration/roundtrip")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if string(got) != want {
		t.Fatalf("real MinIO round trip = %q, want %q", got, want)
	}

	keys, err := tgt.List(ctx, "integration/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, k := range keys {
		if k == "integration/roundtrip" {
			found = true
		}
	}
	if !found {
		t.Fatalf("List(integration/) = %v, want to contain integration/roundtrip", keys)
	}

	if err := tgt.Delete(ctx, "integration/roundtrip"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := tgt.Get(ctx, "integration/roundtrip"); err == nil {
		t.Fatal("Get after Delete against real MinIO returned nil error")
	}
}
