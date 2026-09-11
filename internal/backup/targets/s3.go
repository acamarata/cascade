// Purpose: S3Target (05 §Epic S S-41.T3, §D-15) — the s3 backup Target
// over the real S3 REST API. Per §D-15 and R-16.57: this file constructs
// its OWN minio-go/v7 client instance and does NOT import providers/s3 or
// its server-profile wiring; the only thing shared with that ticket's
// producer role is the wire library dependency itself (already
// license-approved, internal/build/licenses.go) and the general shape of
// an env-ref-resolved connection.
//
// Inputs: S3Config (endpoint/bucket/access-key-id/secret-access-key,
// resolved via ResolveS3TargetEnvRefs — vault/env-ref only, H/S-15.T3) and
// an *egress.Engine (every outbound byte transits Intercept, R-21.265).
// Outputs: real S3 objects, or a typed *cascade.Error.
// Constraints: pure Go, no CGO (06 §2); the credential never reaches an
// error message; unit tests (s3_test.go) run against gofakes3 (a real S3
// REST double, Art.7.2) — the REAL MinIO run is s3_integration_test.go.
//
// SPORT: internal.backup.targets.s3/ADDED (P1-E19-W4-S41-T3).

package targets

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/pkg/cascade"
)

// s3 env-ref suffixes an s3_env_prefix (08 §2) expands to.
const (
	s3EnvSuffixEndpoint = "_ENDPOINT"
	s3EnvSuffixBucket   = "_BUCKET"
	s3EnvSuffixKeyID    = "_KEY_ID"
	s3EnvSuffixSecret   = "_SECRET"
)

// S3Config is this ticket's own, standalone config type for the s3
// target: endpoint/bucket/access-key-id/secret-access-key. It is never a
// literal a caller types by hand in production — ResolveS3TargetEnvRefs
// is the only production constructor — but is exported as a plain struct
// so tests can build one directly against a fake server.
type S3Config struct {
	Endpoint        string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
}

// ResolveS3TargetEnvRefs resolves S3Config's four fields from an
// s3_env_prefix-expanded family of env vars (08 §2): prefix+"_ENDPOINT",
// prefix+"_BUCKET", prefix+"_KEY_ID", prefix+"_SECRET". getenv is
// injected so no test reads the real process environment. Every field
// must be set and non-empty; H/S-15.T3's literal-secret detector refuses
// a caller that hands this a value instead of a reference name, and this
// function never accepts one in the value's place — it only ever reads
// what getenv(name) returns for the names it computes.
func ResolveS3TargetEnvRefs(getenv func(string) string, prefix string) (S3Config, error) {
	if strings.TrimSpace(prefix) == "" {
		return S3Config{}, cascade.New(cascade.KindInvalidInput, "targets: s3 target requires a non-empty env-ref prefix")
	}
	cfg := S3Config{
		Endpoint:        getenv(prefix + s3EnvSuffixEndpoint),
		Bucket:          getenv(prefix + s3EnvSuffixBucket),
		AccessKeyID:     getenv(prefix + s3EnvSuffixKeyID),
		SecretAccessKey: getenv(prefix + s3EnvSuffixSecret),
	}
	if err := cfg.validate(prefix); err != nil {
		return S3Config{}, err
	}
	return cfg, nil
}

// validate reports which of prefix's four env-refs are unset.
func (cfg S3Config) validate(prefix string) error {
	var missing []string
	if cfg.Endpoint == "" {
		missing = append(missing, prefix+s3EnvSuffixEndpoint)
	}
	if cfg.Bucket == "" {
		missing = append(missing, prefix+s3EnvSuffixBucket)
	}
	if cfg.AccessKeyID == "" {
		missing = append(missing, prefix+s3EnvSuffixKeyID)
	}
	if cfg.SecretAccessKey == "" {
		missing = append(missing, prefix+s3EnvSuffixSecret)
	}
	if len(missing) > 0 {
		return cascade.Newf(cascade.KindInvalidInput, "targets: s3 target env-refs unset: %s", strings.Join(missing, ", "))
	}
	return nil
}

// S3Target is the s3 backup Target driver. The zero value is not usable;
// construct with NewS3Target.
type S3Target struct {
	client *minio.Client
	bucket string
	engine *egress.Engine
	cap    egress.Capability
}

// NewS3Target dials cfg's endpoint, confirms cfg.Bucket exists and is
// reachable, and acquires the backup-target egress capability from
// engine — all before returning a usable *S3Target.
func NewS3Target(ctx context.Context, cfg S3Config, engine *egress.Engine) (*S3Target, error) {
	if err := cfg.validate("s3"); err != nil {
		return nil, err
	}
	host, secure, err := parseS3Endpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	token, err := acquireBackupTargetCapability(engine)
	if err != nil {
		return nil, err
	}
	client, err := minio.New(host, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		Secure: secure,
		Region: "us-east-1",
	})
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "targets: s3: constructing client")
	}
	exists, err := client.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, cascade.Wrapf(classifyS3Err(err), err, "targets: s3: connecting to %s", redactS3Endpoint(cfg.Endpoint))
	}
	if !exists {
		return nil, cascade.Newf(cascade.KindNotFound, "targets: s3: bucket %q does not exist or is not accessible", cfg.Bucket)
	}
	return &S3Target{client: client, bucket: cfg.Bucket, engine: engine, cap: token}, nil
}

// parseS3Endpoint splits endpointURL into minio.New's expected host:port
// and whether the connection uses TLS. A bare host:port with no explicit
// scheme is refused, matching providers/s3's own parseEndpoint contract.
func parseS3Endpoint(endpointURL string) (host string, secure bool, err error) {
	u, perr := url.Parse(endpointURL)
	if perr != nil || u.Host == "" {
		return "", false, cascade.Newf(cascade.KindInvalidInput, "targets: s3: parsing endpoint url %q", redactS3Endpoint(endpointURL))
	}
	switch u.Scheme {
	case "https":
		return u.Host, true, nil
	case "http":
		return u.Host, false, nil
	default:
		return "", false, cascade.Newf(cascade.KindInvalidInput, "targets: s3: endpoint url scheme %q must be http or https", u.Scheme)
	}
}

// redactS3Endpoint returns endpointURL with any userinfo stripped, or a
// fixed placeholder if it does not even parse.
func redactS3Endpoint(endpointURL string) string {
	u, err := url.Parse(endpointURL)
	if err != nil {
		return "s3://<redacted>"
	}
	return u.Redacted()
}

// classifyS3Err reports the taxonomy Kind that best fits an S3 API
// error, reading the error's structured S3 REST Code — never message
// text.
func classifyS3Err(err error) cascade.Kind {
	switch {
	case err == nil:
		return cascade.KindInternal
	case errors.Is(err, context.DeadlineExceeded):
		return cascade.KindTimeout
	case errors.Is(err, context.Canceled):
		return cascade.KindCanceled
	}
	switch s3ErrCode(err) {
	case "NoSuchKey", "NoSuchBucket":
		return cascade.KindNotFound
	case "AccessDenied", "SignatureDoesNotMatch", "InvalidAccessKeyId":
		return cascade.KindPermissionDenied
	case "RequestTimeout":
		return cascade.KindTimeout
	default:
		return cascade.KindUnavailable
	}
}

// s3ErrCode returns err's S3 error Code, or "" if it carries none.
func s3ErrCode(err error) string {
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		return resp.Code
	}
	return minio.ToErrorResponse(err).Code
}

// Put implements Target. content is buffered so it can transit
// Intercept before the single PUT: no partial-content object key is ever
// visible to a concurrent Get/List (the key is fixed by the caller, but
// the bytes behind it must clear the firewall first).
func (t *S3Target) Put(ctx context.Context, key string, r io.Reader) error {
	if err := ctx.Err(); err != nil {
		return ctxErr(err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "targets: s3: reading content")
	}
	out, err := interceptOutbound(ctx, t.engine, t.cap, data)
	if err != nil {
		return err
	}
	_, err = t.client.PutObject(ctx, t.bucket, key, bytes.NewReader(out), int64(len(out)), minio.PutObjectOptions{})
	if err != nil {
		return cascade.Wrapf(classifyS3Err(err), err, "targets: s3: put %q", key)
	}
	return nil
}

// Get implements Target.
func (t *S3Target) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, ctxErr(err)
	}
	obj, err := t.client.GetObject(ctx, t.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, cascade.Wrapf(classifyS3Err(err), err, "targets: s3: get %q", key)
	}
	// GetObject is lazy: Stat now so a missing key surfaces here, not on
	// the caller's first Read.
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		if s3ErrCode(err) == "NoSuchKey" {
			return nil, cascade.Newf(cascade.KindNotFound, "targets: s3: %q not found", key)
		}
		return nil, cascade.Wrapf(classifyS3Err(err), err, "targets: s3: get %q", key)
	}
	return obj, nil
}

// List implements Target: every object key under the bucket with the
// given prefix, recursively, sorted (ListObjects's own delivery order).
func (t *S3Target) List(ctx context.Context, prefix string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, ctxErr(err)
	}
	var out []string
	for obj := range t.client.ListObjects(ctx, t.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if obj.Err != nil {
			return nil, cascade.Wrapf(classifyS3Err(obj.Err), obj.Err, "targets: s3: listing %q", prefix)
		}
		out = append(out, obj.Key)
	}
	return out, nil
}

// Delete implements Target. Deleting an absent key is not an error —
// S3's DELETE is idempotent (204 No Content) for a missing key.
func (t *S3Target) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return ctxErr(err)
	}
	if err := t.client.RemoveObject(ctx, t.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return cascade.Wrapf(classifyS3Err(err), err, "targets: s3: delete %q", key)
	}
	return nil
}
