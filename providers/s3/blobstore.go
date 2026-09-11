// Purpose: BlobStore, the provider.BlobStore driver over a live Conn — the
//   server profile's BlobStore leg (P1-E17-W4-S38-T7), carrying the SAME
//   content-addressed (BLAKE3-256) contract as the local providers/fs
//   driver, proven equivalent by the same storetest.RunBlobStoreTests
//   suite.
// Inputs: a *Conn (New) plus namespace/data per call.
// Outputs: a provider.Hash (Put) or a *cascade.Error carrying a taxonomy
//   Kind.
// Constraints: providers/** imports pkg/** only (Art.10.2); the object
//   key layout mirrors providers/fs/blobstore.go's directory sharding
//   scheme (<namespace>/<hash[0:2] hex>/<hash[2:] hex>) so the two drivers
//   are recognizably the same content-addressing design, just over S3
//   keys instead of filesystem paths; unlike providers/fs's write-to-temp-
//   then-rename dance (needed because a filesystem write is not
//   naturally atomic), a single S3 PUT to the final content-addressed key
//   is already atomic per S3's own semantics, so no staging step is
//   needed here.
// SPORT: providers.s3.BlobStore/ADDED (P1-E17-W4-S38-T7).

package s3

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/zeebo/blake3"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// objectKeyPrefix namespaces every object key this driver writes.
const objectKeyPrefix = "cascade/blob/"

// prefixLen is the number of hex characters (one byte) used as the
// object-key sharding prefix, matching providers/fs/blobstore.go's
// prefixLen.
const prefixLen = 2

// BlobStore is the S3-backed provider.BlobStore driver. The zero value is
// not usable; construct with NewBlobStore.
type BlobStore struct {
	conn *Conn
}

// NewBlobStore returns a BlobStore issuing every request over conn.
func NewBlobStore(conn *Conn) *BlobStore {
	return &BlobStore{conn: conn}
}

// ValidNamespace reports whether namespace is safe to use as an object-key
// path component: non-empty, containing no "/" and not "." or "..",
// matching providers/fs.ValidNamespace's contract exactly (the two drivers
// share the same namespace-safety rule even though one addresses a
// filesystem path and the other an S3 key).
func ValidNamespace(namespace string) bool {
	if namespace == "" || namespace == "." || namespace == ".." {
		return false
	}
	return !strings.Contains(namespace, "/")
}

// objectKey returns the content-addressed S3 object key for hash within
// namespace.
func objectKey(namespace string, hash provider.Hash) string {
	hex := hash.String()
	return objectKeyPrefix + namespace + "/" + hex[:prefixLen] + "/" + hex[prefixLen:]
}

// Put implements provider.BlobStore. Content is buffered in memory while
// hashing (io.MultiWriter into both the hasher and the buffer) so the
// final, content-addressed object key is known before the single PUT to
// S3 — no partial-content key is ever visible to a concurrent Get/Exists.
func (b *BlobStore) Put(ctx context.Context, namespace string, data io.Reader) (provider.Hash, error) {
	if err := ctx.Err(); err != nil {
		return provider.Hash{}, cascade.Wrap(cascade.KindCanceled, err, "s3.BlobStore.Put: context")
	}
	if !ValidNamespace(namespace) {
		return provider.Hash{}, cascade.Newf(cascade.KindInvalidInput, "s3.BlobStore.Put: invalid namespace %q", namespace)
	}
	var buf bytes.Buffer
	hasher := blake3.New()
	if _, err := io.Copy(io.MultiWriter(&buf, hasher), data); err != nil {
		return provider.Hash{}, cascade.Wrap(cascade.KindUnavailable, err, "s3.BlobStore.Put: reading content")
	}
	var hash provider.Hash
	copy(hash[:], hasher.Sum(nil))

	key := objectKey(namespace, hash)
	size := int64(buf.Len())
	_, err := b.conn.client.PutObject(ctx, b.conn.bucket, key, &buf, size, minio.PutObjectOptions{})
	if err != nil {
		return provider.Hash{}, cascade.Wrapf(classifyErr(err), err, "s3.BlobStore.Put: namespace %q", namespace)
	}
	return hash, nil
}

// Get implements provider.BlobStore.
func (b *BlobStore) Get(ctx context.Context, namespace string, hash provider.Hash) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindCanceled, err, "s3.BlobStore.Get: context")
	}
	if !ValidNamespace(namespace) {
		return nil, cascade.Newf(cascade.KindInvalidInput, "s3.BlobStore.Get: invalid namespace %q", namespace)
	}
	obj, err := b.conn.client.GetObject(ctx, b.conn.bucket, objectKey(namespace, hash), minio.GetObjectOptions{})
	if err != nil {
		return nil, cascade.Wrapf(classifyErr(err), err, "s3.BlobStore.Get: namespace %q", namespace)
	}
	// GetObject returns lazily: the request is not actually sent, and a
	// NoSuchKey error not actually raised, until the first read. Stat now
	// so a missing hash surfaces here, matching providers/fs.Get's
	// eager-open behavior, rather than on the caller's first Read call.
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		if errResponseCode(err) == "NoSuchKey" {
			return nil, cascade.Newf(cascade.KindNotFound, "s3.BlobStore.Get: hash %s not found in namespace %q", hash, namespace)
		}
		return nil, cascade.Wrapf(classifyErr(err), err, "s3.BlobStore.Get: namespace %q", namespace)
	}
	return obj, nil
}

// errResponseCode returns err's S3 error Code, or "" if err carries none.
func errResponseCode(err error) string {
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		return resp.Code
	}
	return minio.ToErrorResponse(err).Code
}

// Delete implements provider.BlobStore. Deleting an absent hash is not an
// error — S3's own DELETE is idempotent for a missing key (204 No
// Content, never an error), so no special-casing is needed.
func (b *BlobStore) Delete(ctx context.Context, namespace string, hash provider.Hash) error {
	if err := ctx.Err(); err != nil {
		return cascade.Wrap(cascade.KindCanceled, err, "s3.BlobStore.Delete: context")
	}
	if !ValidNamespace(namespace) {
		return cascade.Newf(cascade.KindInvalidInput, "s3.BlobStore.Delete: invalid namespace %q", namespace)
	}
	err := b.conn.client.RemoveObject(ctx, b.conn.bucket, objectKey(namespace, hash), minio.RemoveObjectOptions{})
	if err != nil {
		return cascade.Wrapf(classifyErr(err), err, "s3.BlobStore.Delete: namespace %q", namespace)
	}
	return nil
}

// Exists implements provider.BlobStore.
func (b *BlobStore) Exists(ctx context.Context, namespace string, hash provider.Hash) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, cascade.Wrap(cascade.KindCanceled, err, "s3.BlobStore.Exists: context")
	}
	if !ValidNamespace(namespace) {
		return false, cascade.Newf(cascade.KindInvalidInput, "s3.BlobStore.Exists: invalid namespace %q", namespace)
	}
	_, err := b.conn.client.StatObject(ctx, b.conn.bucket, objectKey(namespace, hash), minio.StatObjectOptions{})
	if err == nil {
		return true, nil
	}
	if errResponseCode(err) == "NoSuchKey" {
		return false, nil
	}
	return false, cascade.Wrapf(classifyErr(err), err, "s3.BlobStore.Exists: namespace %q", namespace)
}
