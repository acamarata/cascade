// Package fs is the filesystem provider.BlobStore driver: content-addressed
// binary storage keyed by the BLAKE3-256 digest of a blob's own bytes
// (providers/fs/ is the ratified home per R-14.6; 02-TARGET-STRUCTURE.md
// §providers).
//
// Hash algorithm: BLAKE3-256, computed by github.com/zeebo/blake3 (public
// domain / CC0-1.0). provider.Hash does not encode the algorithm in its
// type (an accepted risk recorded at CR — pkg/provider/blob.go's own
// godoc), so this is the single place that decision is made for this
// driver: any second BlobStore driver in this module MUST use the same
// BLAKE3-256 algorithm to remain interchangeable, since a Hash computed by
// one driver is meaningless read back through another that disagrees.
//
// Layout: a blob addressed by Hash h is stored at
// <root>/<namespace>/<h[0:2] hex>/<h[2:] hex> — the two-character prefix
// directory keeps any one directory's entry count small even with many
// blobs, a standard content-addressed-storage layout (git's object store
// uses the same scheme). namespace is validated as a single safe path
// component (ValidNamespace) so a caller-supplied namespace can never
// escape root via a path separator or "..".
//
// Idempotency and atomicity: Put streams data into a temp file under
// <root>/.tmp while hashing it, then renames the temp file onto the final
// content-addressed path. os.Rename is atomic on POSIX (same filesystem)
// and replaces an existing destination on both POSIX and Windows, so two
// concurrent Puts of identical content each independently compute the same
// Hash and each rename succeeds — whichever rename lands last simply
// replaces bit-identical content, which is a no-op in every observable
// sense. A driver-level pre-existence check is deliberately not used
// (Puts of different content never share a Hash by definition, and
// checking-then-renaming a same-content Put is not measurably safer, only
// slower under concurrency).
//
// Purpose: concrete providers.fs implementation of pkg/provider.BlobStore.
// Inputs: a root directory (New) plus namespace/data (Put) or
//
//	namespace/Hash (Get/Delete/Exists).
//
// Outputs: a provider.Hash (Put), an io.ReadCloser (Get), or a
//
//	*cascade.Error carrying a taxonomy Kind.
//
// Constraints: providers/** imports pkg/** only, never internal/**
//
//	(Art.10.2); uses only os/path/filepath/io (Art.5 platform parity — no
//	platform-specific syscalls); every test root is a t.TempDir() (Art.7.1).
//
// SPORT: providers.fs.BlobStore/ADDED (P1-E02-W1-S02-T4).
package fs

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/zeebo/blake3"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// tmpDirName is the staging subdirectory Put writes into before the atomic
// rename onto the content-addressed final path. It lives under root so the
// rename never crosses a filesystem boundary (which would break atomicity).
const tmpDirName = ".tmp"

// prefixLen is the number of hex characters (one byte) used as the
// blob-directory sharding prefix.
const prefixLen = 2

// BlobStore is the filesystem-backed provider.BlobStore driver. The zero
// value is not usable; construct with New.
type BlobStore struct {
	root string
}

// New returns a BlobStore rooted at root, creating root and its .tmp
// staging directory if they do not already exist.
func New(root string) (*BlobStore, error) {
	if root == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "fs.BlobStore: root must not be empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindInvalidInput, err, "fs.BlobStore: resolving root %q", root)
	}
	if err := os.MkdirAll(filepath.Join(abs, tmpDirName), 0o755); err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "fs.BlobStore: creating root %q", abs)
	}
	return &BlobStore{root: abs}, nil
}

// ValidNamespace reports whether namespace is safe to use as a single
// filesystem path component: non-empty, containing no path separator (on
// either POSIX or Windows) and not "." or "..".
func ValidNamespace(namespace string) bool {
	if namespace == "" || namespace == "." || namespace == ".." {
		return false
	}
	return !strings.ContainsAny(namespace, "/\\")
}

// blobPath returns the content-addressed path for hash within namespace,
// without checking that it exists.
func (b *BlobStore) blobPath(namespace string, hash provider.Hash) string {
	hex := hash.String()
	return filepath.Join(b.root, namespace, hex[:prefixLen], hex[prefixLen:])
}

// Put implements provider.BlobStore.
func (b *BlobStore) Put(ctx context.Context, namespace string, data io.Reader) (provider.Hash, error) {
	if err := ctx.Err(); err != nil {
		return provider.Hash{}, cascade.Wrap(cascade.KindCanceled, err, "fs.BlobStore.Put: context")
	}
	if !ValidNamespace(namespace) {
		return provider.Hash{}, cascade.Newf(cascade.KindInvalidInput, "fs.BlobStore.Put: invalid namespace %q", namespace)
	}

	tmp, err := os.CreateTemp(filepath.Join(b.root, tmpDirName), "blob-*")
	if err != nil {
		return provider.Hash{}, cascade.Wrap(cascade.KindUnavailable, err, "fs.BlobStore.Put: creating temp file")
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op once renamed away

	hasher := blake3.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hasher), data); err != nil {
		_ = tmp.Close()
		return provider.Hash{}, cascade.Wrap(cascade.KindUnavailable, err, "fs.BlobStore.Put: writing content")
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return provider.Hash{}, cascade.Wrap(cascade.KindUnavailable, err, "fs.BlobStore.Put: syncing content")
	}
	if err := tmp.Close(); err != nil {
		return provider.Hash{}, cascade.Wrap(cascade.KindUnavailable, err, "fs.BlobStore.Put: closing temp file")
	}

	var hash provider.Hash
	copy(hash[:], hasher.Sum(nil))

	finalPath := b.blobPath(namespace, hash)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		return provider.Hash{}, cascade.Wrapf(cascade.KindUnavailable, err, "fs.BlobStore.Put: creating blob directory for namespace %q", namespace)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return provider.Hash{}, cascade.Wrapf(cascade.KindUnavailable, err, "fs.BlobStore.Put: renaming into place for namespace %q", namespace)
	}
	return hash, nil
}

// Get implements provider.BlobStore.
func (b *BlobStore) Get(ctx context.Context, namespace string, hash provider.Hash) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindCanceled, err, "fs.BlobStore.Get: context")
	}
	if !ValidNamespace(namespace) {
		return nil, cascade.Newf(cascade.KindInvalidInput, "fs.BlobStore.Get: invalid namespace %q", namespace)
	}
	f, err := os.Open(b.blobPath(namespace, hash))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, cascade.Newf(cascade.KindNotFound, "fs.BlobStore.Get: hash %s not found in namespace %q", hash, namespace)
		}
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "fs.BlobStore.Get: opening hash %s in namespace %q", hash, namespace)
	}
	return f, nil
}

// Delete implements provider.BlobStore.
func (b *BlobStore) Delete(ctx context.Context, namespace string, hash provider.Hash) error {
	if err := ctx.Err(); err != nil {
		return cascade.Wrap(cascade.KindCanceled, err, "fs.BlobStore.Delete: context")
	}
	if !ValidNamespace(namespace) {
		return cascade.Newf(cascade.KindInvalidInput, "fs.BlobStore.Delete: invalid namespace %q", namespace)
	}
	if err := os.Remove(b.blobPath(namespace, hash)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return cascade.Wrapf(cascade.KindUnavailable, err, "fs.BlobStore.Delete: hash %s in namespace %q", hash, namespace)
	}
	return nil
}

// Exists implements provider.BlobStore.
func (b *BlobStore) Exists(ctx context.Context, namespace string, hash provider.Hash) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, cascade.Wrap(cascade.KindCanceled, err, "fs.BlobStore.Exists: context")
	}
	if !ValidNamespace(namespace) {
		return false, cascade.Newf(cascade.KindInvalidInput, "fs.BlobStore.Exists: invalid namespace %q", namespace)
	}
	_, err := os.Stat(b.blobPath(namespace, hash))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, cascade.Wrapf(cascade.KindUnavailable, err, "fs.BlobStore.Exists: hash %s in namespace %q", hash, namespace)
}

// BlobInfo is the metadata BlobStore.Stat returns about one blob, without
// transferring its content. Stat is not part of the provider.BlobStore
// interface (the ticket's own task text calls it out separately from the
// four interface methods); it is offered as an extra, driver-specific
// convenience for callers that already import this concrete package.
type BlobInfo struct {
	// Size is the blob's content length in bytes.
	Size int64
}

// Stat implements the BlobStore driver's extra metadata accessor (see
// BlobInfo's doc comment).
func (b *BlobStore) Stat(ctx context.Context, namespace string, hash provider.Hash) (BlobInfo, error) {
	if err := ctx.Err(); err != nil {
		return BlobInfo{}, cascade.Wrap(cascade.KindCanceled, err, "fs.BlobStore.Stat: context")
	}
	if !ValidNamespace(namespace) {
		return BlobInfo{}, cascade.Newf(cascade.KindInvalidInput, "fs.BlobStore.Stat: invalid namespace %q", namespace)
	}
	info, err := os.Stat(b.blobPath(namespace, hash))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return BlobInfo{}, cascade.Newf(cascade.KindNotFound, "fs.BlobStore.Stat: hash %s not found in namespace %q", hash, namespace)
		}
		return BlobInfo{}, cascade.Wrapf(cascade.KindUnavailable, err, "fs.BlobStore.Stat: hash %s in namespace %q", hash, namespace)
	}
	return BlobInfo{Size: info.Size()}, nil
}
