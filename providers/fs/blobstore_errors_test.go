// Purpose: BlobStore error-path coverage split out of blobstore_test.go
//   (R-14.117 in-package cap split — this file plus its sibling both stay
//   under Art.10.3's 300-line cap). Covers every cascade.Kind each method
//   can return for a genuine driver failure (not just the happy path and
//   not just the conformance suite's own error cases): context
//   cancellation, missing/misplaced directories, a failing io.Reader, and
//   a Delete target replaced by a non-empty directory.
// Constraints: every root is t.TempDir() (Art.7.1); no network calls; no
//   sleeps as synchronization.
// SPORT: providers.fs.BlobStore/ADDED (P1-E02-W1-S02-T4).

package fs_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/fs"
)

// errReader is an io.Reader that always fails, for exercising Put's
// io.Copy error path.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("errReader: simulated read failure") }

func TestBlobStore_Put_ReadError(t *testing.T) {
	b := newStore(t)
	if _, err := b.Put(context.Background(), "ns", errReader{}); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Put with a failing reader: want KindUnavailable, got %v", err)
	}
}

func TestBlobStore_Put_MissingTmpDir(t *testing.T) {
	root := t.TempDir()
	b, err := fs.New(root)
	if err != nil {
		t.Fatalf("fs.New: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(root, ".tmp")); err != nil {
		t.Fatalf("removing .tmp staging dir: %v", err)
	}
	if _, err := b.Put(context.Background(), "ns", bytes.NewReader([]byte("x"))); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Put with missing .tmp staging dir: want KindUnavailable, got %v", err)
	}
}

func TestBlobStore_New_RootIsAFile(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked-root")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("seeding root-path collision: %v", err)
	}
	if _, err := fs.New(blocked); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("New with a root path that is a plain file: want KindUnavailable, got %v", err)
	}
}

// seedNotADirectory places a regular file at the shard-prefix directory
// path for hash within namespace, so a subsequent Get/Exists/Stat's path
// traversal through it fails with ENOTDIR — an os error that is NOT
// os.ErrNotExist, exercising each method's cascade.KindUnavailable branch.
func seedNotADirectory(t *testing.T, root, namespace string, hash provider.Hash) {
	t.Helper()
	hex := hash.String()
	shardDir := filepath.Join(root, namespace)
	if err := os.MkdirAll(shardDir, 0o755); err != nil {
		t.Fatalf("creating namespace dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(shardDir, hex[:2]), []byte("blocks traversal"), 0o644); err != nil {
		t.Fatalf("seeding not-a-directory collision: %v", err)
	}
}

func TestBlobStore_Get_PathTraversalError(t *testing.T) {
	root := t.TempDir()
	b, err := fs.New(root)
	if err != nil {
		t.Fatalf("fs.New: %v", err)
	}
	var h provider.Hash
	h[0] = 0xAB
	seedNotADirectory(t, root, "ns", h)
	if _, err := b.Get(context.Background(), "ns", h); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Get through a blocked path component: want KindUnavailable, got %v", err)
	}
}

func TestBlobStore_Exists_PathTraversalError(t *testing.T) {
	root := t.TempDir()
	b, err := fs.New(root)
	if err != nil {
		t.Fatalf("fs.New: %v", err)
	}
	var h provider.Hash
	h[0] = 0xAB
	seedNotADirectory(t, root, "ns", h)
	if _, err := b.Exists(context.Background(), "ns", h); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Exists through a blocked path component: want KindUnavailable, got %v", err)
	}
}

func TestBlobStore_Stat_PathTraversalError(t *testing.T) {
	root := t.TempDir()
	b, err := fs.New(root)
	if err != nil {
		t.Fatalf("fs.New: %v", err)
	}
	var h provider.Hash
	h[0] = 0xAB
	seedNotADirectory(t, root, "ns", h)
	if _, err := b.Stat(context.Background(), "ns", h); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Stat through a blocked path component: want KindUnavailable, got %v", err)
	}
}

func TestBlobStore_New_EmptyRoot(t *testing.T) {
	if _, err := fs.New(""); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("New(\"\"): want KindInvalidInput, got %v", err)
	}
}

func TestBlobStore_CanceledContext(t *testing.T) {
	b := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := b.Put(ctx, "ns", bytes.NewReader([]byte("x"))); !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("Put with canceled context: want KindCanceled, got %v", err)
	}
	var h provider.Hash
	if _, err := b.Get(ctx, "ns", h); !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("Get with canceled context: want KindCanceled, got %v", err)
	}
	if err := b.Delete(ctx, "ns", h); !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("Delete with canceled context: want KindCanceled, got %v", err)
	}
	if _, err := b.Exists(ctx, "ns", h); !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("Exists with canceled context: want KindCanceled, got %v", err)
	}
	if _, err := b.Stat(ctx, "ns", h); !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("Stat with canceled context: want KindCanceled, got %v", err)
	}
}

// TestBlobStore_Put_DirectoryCollision exercises Put's "creating blob
// directory" cascade.KindUnavailable error path: namespace already exists
// as a plain file, so MkdirAll of the shard subdirectory beneath it fails.
func TestBlobStore_Put_DirectoryCollision(t *testing.T) {
	root := t.TempDir()
	b, err := fs.New(root)
	if err != nil {
		t.Fatalf("fs.New: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "blocked-ns"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("seeding namespace-path collision: %v", err)
	}
	ctx := context.Background()
	if _, err := b.Put(ctx, "blocked-ns", bytes.NewReader([]byte("x"))); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Put with namespace/file collision: want KindUnavailable, got %v", err)
	}
}

// TestBlobStore_Delete_NonEmptyDirectory exercises Delete's
// cascade.KindUnavailable path for an os.Remove failure that is NOT
// os.ErrNotExist: the content-addressed path has been replaced (out of
// band, simulating filesystem corruption) with a non-empty directory,
// which os.Remove refuses to remove.
func TestBlobStore_Delete_NonEmptyDirectory(t *testing.T) {
	root := t.TempDir()
	b, err := fs.New(root)
	if err != nil {
		t.Fatalf("fs.New: %v", err)
	}
	ctx := context.Background()
	hash, err := b.Put(ctx, "ns", bytes.NewReader([]byte("to be corrupted")))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	hex := hash.String()
	path := filepath.Join(root, "ns", hex[:2], hex[2:])
	if err := os.Remove(path); err != nil {
		t.Fatalf("removing blob file to corrupt layout: %v", err)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("replacing blob path with a directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "child"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seeding non-empty directory: %v", err)
	}

	if err := b.Delete(ctx, "ns", hash); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Delete of a non-empty directory in place of a blob: want KindUnavailable, got %v", err)
	}
}
