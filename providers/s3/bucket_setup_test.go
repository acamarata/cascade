//go:build integration

// Purpose: the s3 lane's bucket setup, done in Go with the same S3 client
//
//	the driver uses, so a setup failure fails the lane instead of passing
//	silently in a shell step. ensureTestBucket creates the bucket when it
//	is absent; requireTestBucket and requireLaneBucket turn any setup
//	error into a test failure, never a skip.
//
// Inputs: a realS3Config (the CASCADE_TEST_S3_* env-refs, see
//
//	integration_test.go).
//
// Constraints: go:build integration only; writes nothing outside the
//
//	server; no credential ever reaches an error message.
package s3_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// bucketSetupTimeout bounds one setup attempt. minio-go retries with
// backoff, so an unreachable server must hit this deadline, not hang.
const bucketSetupTimeout = 30 * time.Second

// laneBucket caches the lane's single setup of the env-ref bucket, so
// every live test shares one attempt and one verdict.
var laneBucket struct {
	once sync.Once
	err  error
}

// ensureTestBucket makes sure cfg.bucket exists on cfg.endpoint. It is
// idempotent: an existing bucket, or a concurrent create that already
// succeeded, counts as success. Any other failure comes back wrapped
// with the endpoint and bucket, never a credential.
func ensureTestBucket(ctx context.Context, cfg realS3Config) error {
	client, endpoint, err := newTestClient(cfg)
	if err != nil {
		return err
	}
	exists, err := client.BucketExists(ctx, cfg.bucket)
	if err != nil {
		return fmt.Errorf("s3 bucket setup: checking bucket %q at %s: %w", cfg.bucket, endpoint, err)
	}
	if exists {
		return nil
	}
	err = client.MakeBucket(ctx, cfg.bucket, minio.MakeBucketOptions{Region: "us-east-1"})
	if err != nil && minio.ToErrorResponse(err).Code != minio.BucketAlreadyOwnedByYou {
		return fmt.Errorf("s3 bucket setup: creating bucket %q at %s: %w", cfg.bucket, endpoint, err)
	}
	return nil
}

// newTestClient builds a client with the same minio.New options s3.Open
// uses, and returns the endpoint with any userinfo redacted for use in
// error messages. s3.Open's endpoint parser is unexported, so host and
// scheme come from net/url here.
func newTestClient(cfg realS3Config) (*minio.Client, string, error) {
	u, err := url.Parse(cfg.endpoint)
	if err != nil {
		return nil, "", fmt.Errorf("s3 bucket setup: endpoint is not a URL: %w", err)
	}
	endpoint := u.Redacted()
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, endpoint, fmt.Errorf("s3 bucket setup: endpoint %s must be an http:// or https:// URL", endpoint)
	}
	client, err := minio.New(u.Host, &minio.Options{
		Creds:     credentials.NewStaticV4(cfg.accessKey, cfg.secretKey, ""),
		Secure:    u.Scheme == "https",
		Transport: http.DefaultTransport.(*http.Transport).Clone(),
		Region:    "us-east-1",
	})
	if err != nil {
		return nil, endpoint, fmt.Errorf("s3 bucket setup: building client for %s: %w", endpoint, err)
	}
	return client, endpoint, nil
}

// requireTestBucket runs one uncached setup of cfg's bucket and fails tb
// on any error. It never skips: a lane whose bucket cannot be set up
// must go red.
func requireTestBucket(tb testing.TB, cfg realS3Config) {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), bucketSetupTimeout)
	defer cancel()
	if err := ensureTestBucket(ctx, cfg); err != nil {
		failBucketSetup(tb, err)
	}
}

// requireLaneBucket sets up the lane's env-ref bucket once per test
// binary and fails every caller while that setup's error stands.
func requireLaneBucket(t *testing.T, cfg realS3Config) {
	t.Helper()
	laneBucket.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), bucketSetupTimeout)
		defer cancel()
		laneBucket.err = ensureTestBucket(ctx, cfg)
	})
	if laneBucket.err != nil {
		failBucketSetup(t, laneBucket.err)
	}
}

// failBucketSetup is the one place a setup error becomes a test verdict.
func failBucketSetup(tb testing.TB, err error) {
	tb.Helper()
	tb.Fatalf("s3 bucket setup failed; the lane must fail, never skip: %v", err)
}

// recordingTB records Fatal and Skip calls instead of acting on them, so
// a test can prove which one requireTestBucket reaches for.
type recordingTB struct {
	testing.TB
	fatals []string
	skips  []string
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Fatalf(format string, args ...any) {
	r.fatals = append(r.fatals, fmt.Sprintf(format, args...))
}

func (r *recordingTB) Fatal(args ...any) { r.fatals = append(r.fatals, fmt.Sprint(args...)) }

func (r *recordingTB) FailNow() { r.fatals = append(r.fatals, "FailNow") }

func (r *recordingTB) Skipf(format string, args ...any) {
	r.skips = append(r.skips, fmt.Sprintf(format, args...))
}

func (r *recordingTB) Skip(args ...any) { r.skips = append(r.skips, fmt.Sprint(args...)) }

func (r *recordingTB) SkipNow() { r.skips = append(r.skips, "SkipNow") }

// TestEnsureTestBucket creates a fresh bucket on the real server, proves
// a second call is a no-op, and checks the bucket really exists.
func TestEnsureTestBucket(t *testing.T) {
	cfg := realS3(t)
	cfg.bucket = fmt.Sprintf("cascade-ensure-%d", time.Now().UnixNano())
	client, _, err := newTestClient(cfg)
	if err != nil || client == nil {
		t.Fatalf("newTestClient: client=%v err=%v", client, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if exists, err := client.BucketExists(ctx, cfg.bucket); err != nil || exists {
		t.Fatalf("precondition: BucketExists(%q) = %v, %v; want false, nil", cfg.bucket, exists, err)
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		if err := client.RemoveBucket(cctx, cfg.bucket); err != nil {
			t.Errorf("cleanup RemoveBucket(%q): %v", cfg.bucket, err)
		}
	})
	for i := 1; i <= 2; i++ {
		if err := ensureTestBucket(ctx, cfg); err != nil {
			t.Fatalf("ensureTestBucket call %d: %v", i, err)
		}
	}
	exists, err := client.BucketExists(ctx, cfg.bucket)
	if err != nil || !exists {
		t.Fatalf("after ensureTestBucket: BucketExists(%q) = %v, %v; want true, nil", cfg.bucket, exists, err)
	}
}

// TestEnsureTestBucket_FailurePath proves every failing config returns
// an error that names the endpoint and never the secret, and that
// requireTestBucket fails the test rather than skipping it.
func TestEnsureTestBucket_FailurePath(t *testing.T) {
	good := realS3(t)
	wrongSecret := good
	wrongSecret.secretKey = good.secretKey + "-wr" + "ong"
	unreachable := good
	unreachable.endpoint = "http://127.0.0.1:1"
	badName := good
	badName.bucket = "Invalid_Bucket..Name"
	noScheme := good
	noScheme.endpoint = strings.TrimPrefix(strings.TrimPrefix(good.endpoint, "http://"), "https://")
	cases := []struct {
		name string
		cfg  realS3Config
	}{
		{"unreachable endpoint", unreachable},
		{"wrong secret key", wrongSecret},
		{"invalid bucket name", badName},
		{"endpoint without scheme", noScheme},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := ensureTestBucket(ctx, tc.cfg)
			if err == nil {
				t.Fatalf("ensureTestBucket(%s) = nil, want an error", tc.name)
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.cfg.endpoint) {
				t.Fatalf("error does not name the endpoint %q: %q", tc.cfg.endpoint, msg)
			}
			if tc.cfg.secretKey == "" || strings.Contains(msg, tc.cfg.secretKey) {
				t.Fatalf("error leaks the secret key (or the secret is empty): %q", msg)
			}
			if tc.cfg.accessKey == "" || strings.Contains(msg, tc.cfg.accessKey) {
				t.Fatalf("error leaks the access key (or the key is empty): %q", msg)
			}
		})
	}
	for _, tc := range cases[1:3] {
		t.Run("requireTestBucket fatals on "+tc.name, func(t *testing.T) {
			rec := &recordingTB{TB: t}
			requireTestBucket(rec, tc.cfg)
			if len(rec.fatals) != 1 || len(rec.skips) != 0 {
				t.Fatalf("requireTestBucket recorded fatals=%q skips=%q; want exactly one Fatalf and no Skip",
					rec.fatals, rec.skips)
			}
		})
	}
}
