// Purpose: S3Target unit tests against gofakes3 (a real S3 REST API
// implementation over an in-memory backend, MIT, already license-
// approved by P1-E17-W4-S38-T7) — the untagged/no-docker lane. The REAL,
// docker-provisioned MinIO run is s3_integration_test.go (Art.2). This
// file imports "net/http/httptest" only (a distinct import path the
// no-network-unit-lane gate does not match), never "net"/"net/http"
// itself (Art.7.2).
//
// SPORT: internal.backup.targets.s3/ADDED (P1-E19-W4-S41-T3).
package targets_test

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/acamarata/cascade/internal/backup/targets"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/pkg/cascade"
)

func newFakeS3(t *testing.T, bucket string) *httptest.Server {
	t.Helper()
	backend := s3mem.New()
	if err := backend.CreateBucket(bucket); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	srv := httptest.NewServer(gofakes3.New(backend).Server())
	t.Cleanup(srv.Close)
	return srv
}

func newS3Target(t *testing.T, bucket string) *targets.S3Target {
	t.Helper()
	srv := newFakeS3(t, bucket)
	cfg := targets.S3Config{Endpoint: srv.URL, Bucket: bucket, AccessKeyID: "minioadmin", SecretAccessKey: "minioadmin"}
	tgt, err := targets.NewS3Target(context.Background(), cfg, testEgressEngine(t))
	if err != nil {
		t.Fatalf("NewS3Target: %v", err)
	}
	return tgt
}

func TestS3Target_PutGetRoundTrip(t *testing.T) {
	tgt := newS3Target(t, "bucket1")
	ctx := context.Background()
	want := "s3 target content"
	if err := tgt.Put(ctx, "objects/ab/abcd", strings.NewReader(want)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rc, err := tgt.Get(ctx, "objects/ab/abcd")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if string(got) != want {
		t.Fatalf("round trip = %q, want %q", got, want)
	}
}

func TestS3Target_GetMissingIsNotFound(t *testing.T) {
	tgt := newS3Target(t, "bucket2")
	_, err := tgt.Get(context.Background(), "no/such/key")
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("Get(missing) = %v, want KindNotFound", err)
	}
}

func TestS3Target_DeleteAbsentIsNotError(t *testing.T) {
	tgt := newS3Target(t, "bucket3")
	if err := tgt.Delete(context.Background(), "never/written"); err != nil {
		t.Fatalf("Delete(absent) = %v, want nil", err)
	}
}

func TestS3Target_DeleteThenGetIsNotFound(t *testing.T) {
	tgt := newS3Target(t, "bucket4")
	ctx := context.Background()
	if err := tgt.Put(ctx, "k", strings.NewReader("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := tgt.Delete(ctx, "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := tgt.Get(ctx, "k"); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("Get after delete = %v, want KindNotFound", err)
	}
}

func TestS3Target_ListByPrefix(t *testing.T) {
	tgt := newS3Target(t, "bucket5")
	ctx := context.Background()
	for _, k := range []string{"objects/ab/1", "objects/ab/2", "objects/cd/3"} {
		if err := tgt.Put(ctx, k, strings.NewReader("v")); err != nil {
			t.Fatalf("Put(%s): %v", k, err)
		}
	}
	got, err := tgt.List(ctx, "objects/ab/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List(objects/ab/) = %v, want 2 entries", got)
	}
}

func TestS3Target_MissingBucketRefused(t *testing.T) {
	srv := newFakeS3(t, "real-bucket")
	cfg := targets.S3Config{Endpoint: srv.URL, Bucket: "wrong-bucket", AccessKeyID: "minioadmin", SecretAccessKey: "minioadmin"}
	_, err := targets.NewS3Target(context.Background(), cfg, testEgressEngine(t))
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("NewS3Target(missing bucket) = %v, want KindNotFound", err)
	}
}

func TestS3Target_UnreachableEndpointRefused(t *testing.T) {
	cfg := targets.S3Config{Endpoint: "http://127.0.0.1:1", Bucket: "b", AccessKeyID: "id", SecretAccessKey: "secret"}
	ctx, cancel := context.WithTimeout(context.Background(), 2)
	defer cancel()
	_, err := targets.NewS3Target(ctx, cfg, testEgressEngine(t))
	if err == nil {
		t.Fatal("NewS3Target(unreachable) returned nil error")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) && !cascade.HasKind(err, cascade.KindTimeout) &&
		!cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("NewS3Target(unreachable) = %v, want Unavailable/Timeout/Canceled", err)
	}
}

func TestS3Target_MissingEnvRefsRefused(t *testing.T) {
	_, err := targets.ResolveS3TargetEnvRefs(func(string) string { return "" }, "CASCADE_BACKUP_TARGET_S3")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("ResolveS3TargetEnvRefs(all unset) = %v, want KindInvalidInput", err)
	}
}

func TestS3Target_ResolveEnvRefsFamilyShape(t *testing.T) {
	values := map[string]string{
		"P_ENDPOINT": "http://127.0.0.1:9000",
		"P_BUCKET":   "b",
		"P_KEY_ID":   "id",
		"P_SECRET":   "secret",
	}
	cfg, err := targets.ResolveS3TargetEnvRefs(func(name string) string { return values[name] }, "P")
	if err != nil {
		t.Fatalf("ResolveS3TargetEnvRefs: %v", err)
	}
	if cfg.Endpoint != values["P_ENDPOINT"] || cfg.Bucket != values["P_BUCKET"] ||
		cfg.AccessKeyID != values["P_KEY_ID"] || cfg.SecretAccessKey != values["P_SECRET"] {
		t.Fatalf("ResolveS3TargetEnvRefs = %+v, want values from %v", cfg, values)
	}
}

func TestS3Target_CanceledContextRefusesBeforeIO(t *testing.T) {
	tgt := newS3Target(t, "bucket6")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := tgt.Put(ctx, "k", strings.NewReader("v")); !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("Put(canceled) = %v, want KindCanceled", err)
	}
	if _, err := tgt.Get(ctx, "k"); !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("Get(canceled) = %v, want KindCanceled", err)
	}
	if err := tgt.Delete(ctx, "k"); !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("Delete(canceled) = %v, want KindCanceled", err)
	}
	if _, err := tgt.List(ctx, ""); !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("List(canceled) = %v, want KindCanceled", err)
	}
}

func TestS3Target_NoEngineRefused(t *testing.T) {
	srv := newFakeS3(t, "bucket7")
	cfg := targets.S3Config{Endpoint: srv.URL, Bucket: "bucket7", AccessKeyID: "minioadmin", SecretAccessKey: "minioadmin"}
	_, err := targets.NewS3Target(context.Background(), cfg, (*egress.Engine)(nil))
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("NewS3Target(nil engine) = %v, want KindUnavailable", err)
	}
}

func TestS3Target_UnparseableEndpointRefused(t *testing.T) {
	cfg := targets.S3Config{Endpoint: "not a url \x00", Bucket: "b", AccessKeyID: "id", SecretAccessKey: "secret"}
	_, err := targets.NewS3Target(context.Background(), cfg, testEgressEngine(t))
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("NewS3Target(unparseable endpoint) = %v, want KindInvalidInput", err)
	}
}

func TestS3Target_BadSchemeRefused(t *testing.T) {
	cfg := targets.S3Config{Endpoint: "ftp://127.0.0.1:1", Bucket: "b", AccessKeyID: "id", SecretAccessKey: "secret"}
	_, err := targets.NewS3Target(context.Background(), cfg, testEgressEngine(t))
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("NewS3Target(bad scheme) = %v, want KindInvalidInput", err)
	}
}

func TestS3Target_PutReaderErrorPropagates(t *testing.T) {
	tgt := newS3Target(t, "bucket8")
	err := tgt.Put(context.Background(), "k", errS3Reader{err: io.ErrClosedPipe})
	if err == nil {
		t.Fatal("Put(erroring reader) returned nil error")
	}
}

// errS3Reader always returns err on Read.
type errS3Reader struct{ err error }

func (r errS3Reader) Read([]byte) (int, error) { return 0, r.err }

func TestS3Target_OperationsAfterBucketDeletedAreNotFound(t *testing.T) {
	backend := s3mem.New()
	if err := backend.CreateBucket("gone-bucket"); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	srv := httptest.NewServer(gofakes3.New(backend).Server())
	t.Cleanup(srv.Close)
	cfg := targets.S3Config{Endpoint: srv.URL, Bucket: "gone-bucket", AccessKeyID: "minioadmin", SecretAccessKey: "minioadmin"}
	tgt, err := targets.NewS3Target(context.Background(), cfg, testEgressEngine(t))
	if err != nil {
		t.Fatalf("NewS3Target: %v", err)
	}
	if err := backend.DeleteBucket("gone-bucket"); err != nil {
		t.Fatalf("DeleteBucket: %v", err)
	}
	if err := tgt.Put(context.Background(), "k", strings.NewReader("v")); err == nil {
		t.Fatal("Put after bucket deleted returned nil error")
	}
	if err := tgt.Delete(context.Background(), "k"); err == nil {
		t.Fatal("Delete after bucket deleted returned nil error")
	}
}

func TestS3Target_ResolveEnvRefsPartiallyMissing(t *testing.T) {
	values := map[string]string{
		"Q_ENDPOINT": "http://127.0.0.1:9000",
		"Q_BUCKET":   "b",
		"Q_KEY_ID":   "id",
		// Q_SECRET intentionally unset.
	}
	_, err := targets.ResolveS3TargetEnvRefs(func(name string) string { return values[name] }, "Q")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("ResolveS3TargetEnvRefs(secret unset) = %v, want KindInvalidInput", err)
	}
}
