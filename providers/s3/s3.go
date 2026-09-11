// Package s3 is the S3 provider.BlobStore driver (providers/s3/ per
// 02-TARGET-STRUCTURE §providers): the server profile's BlobStore leg
// (P1-E17-W4-S38-T7), mirroring providers/redis's shape (P1-E17-W4-S38-T6)
// for the Cache+Queue legs and providers/fs's content-addressed contract
// for the BlobStore family itself.
//
// Wire library: github.com/minio/minio-go/v7 (Apache-2.0), a pure-Go S3
// client speaking the real S3 REST API against any S3-compatible
// endpoint — no CGO in this package (06 §2).
//
// Purpose: shared S3 connection lifecycle + error classification for the
//
//	BlobStore driver in blobstore.go.
//
// Inputs: an endpoint URL (naming its scheme and host:port), a bucket
//
//	name, and an access key ID + secret access key (Open).
//
// Outputs: a *Conn wrapping a live client whose target bucket has been
//
//	confirmed to exist and be reachable, or a *cascade.Error carrying a
//	taxonomy Kind.
//
// Constraints: providers/** imports pkg/** only, never internal/**
//
//	(Art.10.2); no CGO (06 §2); no credential (access key ID, secret
//	access key) ever reaches an error message.
//
// SPORT: providers.s3/ADDED (P1-E17-W4-S38-T7).
package s3

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Conn is the shared, live S3 connection the BlobStore driver in this
// package wraps. The zero value is not usable; construct with Open.
type Conn struct {
	client    *minio.Client
	bucket    string
	transport *http.Transport
}

// Open parses endpointURL — an explicit "http://" or "https://" URL
// naming an S3-compatible service's own host:port root, never
// AWS-specific and never a bare host:port with no scheme — dials the
// server with the given access key ID and secret access key,
// and confirms bucket exists and is reachable before returning. Any
// failure — an unparseable URL, an unreachable endpoint, a rejected
// credential, or a missing/inaccessible bucket — is a typed, fail-closed
// *cascade.Error, never a *Conn a caller could mistake for a working one.
func Open(ctx context.Context, endpointURL, bucket, accessKeyID, secretAccessKey string) (*Conn, error) {
	if strings.TrimSpace(endpointURL) == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "s3.Open: endpoint url must not be empty")
	}
	if strings.TrimSpace(bucket) == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "s3.Open: bucket must not be empty")
	}
	host, secure, err := parseEndpoint(endpointURL)
	if err != nil {
		return nil, err
	}
	// A Transport this package owns (rather than minio's own default) lets
	// Close release idle connections through the standard
	// net/http.Transport.CloseIdleConnections hook — minio.Client itself
	// exposes no such method.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	client, err := minio.New(host, &minio.Options{
		Creds:     credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure:    secure,
		Transport: transport,
		// Region fixed rather than left empty: an empty Region makes every
		// request (including BucketExists) first issue a GetBucketLocation
		// probe to discover it. S3-compatible services outside AWS (MinIO
		// included) commonly default to this region regardless, and fixing
		// it here removes one round-trip per Conn without changing
		// behavior against a real server that accepts it.
		Region: "us-east-1",
	})
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "s3.Open: constructing client")
	}
	exists, err := client.BucketExists(ctx, bucket)
	if err != nil {
		return nil, wrapConnError(err, endpointURL, "s3.Open: connecting to")
	}
	if !exists {
		return nil, cascade.Newf(cascade.KindNotFound, "s3.Open: bucket %q does not exist or is not accessible", bucket)
	}
	return &Conn{client: client, bucket: bucket, transport: transport}, nil
}

// parseEndpoint splits endpointURL into the host:port minio.New expects
// and whether the connection should use TLS, per the URL's own scheme.
// A bare "host:port" with no "http://"/"https://" prefix is refused
// rather than guessed at: net/url.Parse itself rejects that shape (a
// colon before the first "/" reads as a scheme-and-opaque URL, not an
// authority — verified by probe, not assumed), so requiring an explicit
// scheme here matches what a caller can actually rely on, rather than
// silently tolerating a shape that fails one call earlier for unrelated
// reasons.
func parseEndpoint(endpointURL string) (host string, secure bool, err error) {
	u, perr := url.Parse(endpointURL)
	if perr != nil || u.Host == "" {
		return "", false, cascade.Newf(cascade.KindInvalidInput, "s3.Open: parsing endpoint url %q", redactEndpoint(endpointURL))
	}
	switch u.Scheme {
	case "https":
		secure = true
	case "http":
		secure = false
	default:
		return "", false, cascade.Newf(cascade.KindInvalidInput, "s3.Open: endpoint url scheme %q must be http or https", u.Scheme)
	}
	return u.Host, secure, nil
}

// Close releases the underlying transport's idle connections. minio.Client
// itself has no Close method (it wraps net/http.Client), so Open keeps its
// own *http.Transport reference specifically so Close has something real
// to release — matching providers/redis.Conn.Close's shape (nil-safe, no
// error).
func (c *Conn) Close() error {
	if c == nil || c.transport == nil {
		return nil
	}
	c.transport.CloseIdleConnections()
	return nil
}

// String returns a short diagnostic label. It never includes the endpoint
// URL or bucket this Conn was opened with.
func (c *Conn) String() string { return "s3.Conn" }

// classifyErr reports the taxonomy Kind that best fits an S3 API error.
// Classification reads the error's structured shape — a *minio.ErrorResponse
// carrying the S3 REST API's own error Code — never the message text,
// matching providers/postgres/postgres_errors.go's classify-by-structure
// precedent.
func classifyErr(err error) cascade.Kind {
	switch {
	case err == nil:
		return cascade.KindInternal
	case errors.Is(err, context.DeadlineExceeded):
		return cascade.KindTimeout
	case errors.Is(err, context.Canceled):
		return cascade.KindCanceled
	}
	resp := minio.ToErrorResponse(err)
	switch resp.Code {
	case "NoSuchKey", "NoSuchBucket":
		return cascade.KindNotFound
	case "AccessDenied", "SignatureDoesNotMatch", "InvalidAccessKeyId":
		return cascade.KindPermissionDenied
	case "RequestTimeout":
		return cascade.KindTimeout
	case "":
		// ToErrorResponse's own zero-value fallback: err did not carry a
		// structured S3 error at all (a dial failure, DNS error, or other
		// transport-level problem before the server ever replied).
		return cascade.KindUnavailable
	default:
		return cascade.KindUnavailable
	}
}

// wrapConnError wraps a connection-establishment error (Open's
// BucketExists probe), taking endpointURL only to compute its redacted
// form for the message — no credential is ever interpolated (this
// package never puts one in the URL itself, but the endpoint host/port
// alone is still redacted defensively, matching providers/postgres's
// wrapConnError/providers/redis's wrapConnError).
func wrapConnError(err error, endpointURL, msg string) error {
	return cascade.Wrapf(classifyErr(err), err, "%s %s", msg, redactEndpoint(endpointURL))
}

// redactedEndpointPlaceholder stands in for a URL net/url cannot parse at
// all.
const redactedEndpointPlaceholder = "s3://<redacted>"

// redactEndpoint returns endpointURL with any userinfo removed via
// net/url.URL.Redacted() — this package never places a credential in the
// endpoint URL itself, but every other driver in this module redacts its
// connection string before it can reach an error message, and this keeps
// that invariant true here too regardless of what a caller passes.
func redactEndpoint(endpointURL string) string {
	u, err := url.Parse(endpointURL)
	if err != nil {
		return redactedEndpointPlaceholder
	}
	return u.Redacted()
}
