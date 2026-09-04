package memory

// Purpose: FileStore, the files-first MemoryStore. The file on disk is the
//   record; every other representation of a memory is derived from it and
//   can be thrown away and rebuilt from the tree alone.
// Inputs: a base directory, an injected Clock, and caller records.
// Outputs: records on disk under {base}/{kind}/{name}.md, and typed
//   pkg/cascade errors.
// Constraints: writes are atomic (temp plus rename in the same directory);
//   reads fail closed on anything they cannot parse whole; timestamps come
//   from the injected clock only; listings are lexically ordered so output
//   does not vary between runs.
// SPORT: G/memory-store (ADD, placeholder per T-1 sport_updates).

import (
	"bytes"
	"context"
	"path/filepath"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// entrySuffix and tombstoneSuffix are the two file shapes the store
// writes. A tombstone sits beside the record it retires rather than
// replacing it, so a projection scanning the tree sees the deletion
// without needing a second source to diff against.
const (
	entrySuffix     = ".md"
	tombstoneSuffix = ".md.tombstone"
)

// FileStore is the canonical MemoryStore: a directory tree in which each
// record is one markdown file with a frontmatter header.
//
// The files are authoritative. When a derived representation of a record
// (an index row, an embedding, a database projection) disagrees with the
// file, the file is right and the derived copy is stale; the correct
// response is to rebuild the derived copy, never to write the file back
// from it. The whole tree can be deleted from a database and rebuilt by
// walking this directory, and nothing outside these files needs to survive
// for that rebuild to be complete.
type FileStore struct {
	base  string
	clock Clock
	fs    fileSystem
}

// Compile-time proof that FileStore satisfies the contract it is written
// against, so a drifting method set fails the build rather than a caller.
var _ MemoryStore = (*FileStore)(nil)

// NewFileStore returns a FileStore rooted at base, taking its timestamps
// from clk. It creates nothing on disk until the first write.
func NewFileStore(base string, clk Clock) *FileStore {
	return newFileStoreWithFS(base, clk, osFS{})
}

// newFileStoreWithFS is NewFileStore with the file-system seam supplied.
// Unexported: tests use it to inject a failing file system, and no shipped
// path may substitute anything for osFS.
func newFileStoreWithFS(base string, clk Clock, sys fileSystem) *FileStore {
	return &FileStore{base: base, clock: clk, fs: sys}
}

// entryPath returns the on-disk path of a record. Both kind and name are
// validated before this is called, so neither can contain a separator and
// the result is always inside base.
func (s *FileStore) entryPath(kind MemoryKind, name string) string {
	return filepath.Join(s.base, string(kind), name+entrySuffix)
}

// tombstonePath returns the tombstone path for a record.
func (s *FileStore) tombstonePath(kind MemoryKind, name string) string {
	return filepath.Join(s.base, string(kind), name+tombstoneSuffix)
}

// checkKey validates the identity half of a request. It runs before any
// path is built, which is what keeps a caller-supplied name from reaching
// the file system at all when it is not a safe path segment.
func checkKey(kind MemoryKind, name string) error {
	if !kind.Valid() {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidKind,
			"unknown memory kind %q", string(kind))
	}
	return ValidateName(name)
}

// Write creates or updates a record, idempotently.
func (s *FileStore) Write(ctx context.Context, entry MemoryEntry) error {
	if err := ctx.Err(); err != nil {
		return cascade.Wrap(cascade.KindCanceled, err, "memory write canceled")
	}
	now := s.clock.Now().UTC()
	if err := entry.Validate(now); err != nil {
		return err
	}
	stored, found, err := s.load(entry.Kind, entry.Name)
	if err != nil {
		return err
	}
	next := entry.canonical()
	next.Provenance.ContentHash = HashBody(next.Body)
	if found {
		return s.writeOver(stored, next, now)
	}
	next.Provenance.CreatedAt = now
	next.Provenance.UpdatedAt = now
	return s.persist(next)
}

// writeOver rewrites an existing record, or does nothing when the record
// on disk already says exactly what this write says.
//
// The comparison is byte equality of the canonical encoding with the
// stored UpdatedAt held fixed, which is stricter than comparing body
// hashes alone. A body-hash-only comparison would treat a changed
// description or confidence as "no change" and silently drop it, and
// dropping a field the caller asked to persist is the failure this store
// exists to not have.
func (s *FileStore) writeOver(stored, next MemoryEntry, now time.Time) error {
	next.Provenance.CreatedAt = stored.Provenance.CreatedAt
	next.Provenance.UpdatedAt = stored.Provenance.UpdatedAt
	if bytes.Equal(encodeEntry(next), encodeEntry(stored)) {
		return nil
	}
	next.Provenance.UpdatedAt = now
	return s.persist(next)
}

// persist writes the record and clears any tombstone, so writing a name
// that was previously deleted brings it back rather than leaving it
// permanently unreachable.
func (s *FileStore) persist(e MemoryEntry) error {
	path := s.entryPath(e.Kind, e.Name)
	if err := s.fs.WriteAtomic(path, encodeEntry(e)); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, ErrStoreIO,
			"writing memory record %s/%s: %v", e.Kind, e.Name, err)
	}
	if err := s.fs.Remove(s.tombstonePath(e.Kind, e.Name)); err != nil && !isNotExist(err) {
		return cascade.Wrapf(cascade.KindUnavailable, ErrStoreIO,
			"clearing tombstone for %s/%s: %v", e.Kind, e.Name, err)
	}
	return nil
}

// load reads a record if one is live. found is false for a record that is
// absent or tombstoned; an unreadable file is an error, never a silent
// "absent", because treating a damaged record as missing is how a write
// overwrites something it could not read.
func (s *FileStore) load(kind MemoryKind, name string) (MemoryEntry, bool, error) {
	if err := checkKey(kind, name); err != nil {
		return MemoryEntry{}, false, err
	}
	tombstoned, err := s.fs.Exists(s.tombstonePath(kind, name))
	if err != nil {
		return MemoryEntry{}, false, cascade.Wrapf(cascade.KindUnavailable, ErrStoreIO,
			"checking tombstone for %s/%s: %v", kind, name, err)
	}
	if tombstoned {
		return MemoryEntry{}, false, nil
	}
	data, err := s.fs.ReadFile(s.entryPath(kind, name))
	if err != nil {
		if isNotExist(err) {
			return MemoryEntry{}, false, nil
		}
		return MemoryEntry{}, false, cascade.Wrapf(cascade.KindUnavailable, ErrStoreIO,
			"reading memory record %s/%s: %v", kind, name, err)
	}
	e, err := decodeEntry(data)
	if err != nil {
		return MemoryEntry{}, false, err
	}
	return e, true, nil
}

// Read returns a live record, or ErrNoSuchEntry.
func (s *FileStore) Read(ctx context.Context, kind MemoryKind, name string) (MemoryEntry, error) {
	if err := ctx.Err(); err != nil {
		return MemoryEntry{}, cascade.Wrap(cascade.KindCanceled, err, "memory read canceled")
	}
	e, found, err := s.load(kind, name)
	if err != nil {
		return MemoryEntry{}, err
	}
	if !found {
		return MemoryEntry{}, cascade.Wrapf(cascade.KindNotFound, ErrNoSuchEntry,
			"no memory record %s/%s", kind, name)
	}
	return e, nil
}

// Update rewrites a record that must already exist.
func (s *FileStore) Update(ctx context.Context, entry MemoryEntry) error {
	if err := ctx.Err(); err != nil {
		return cascade.Wrap(cascade.KindCanceled, err, "memory update canceled")
	}
	now := s.clock.Now().UTC()
	if err := entry.Validate(now); err != nil {
		return err
	}
	stored, found, err := s.load(entry.Kind, entry.Name)
	if err != nil {
		return err
	}
	if !found {
		return cascade.Wrapf(cascade.KindNotFound, ErrNoSuchEntry,
			"cannot update absent memory record %s/%s", entry.Kind, entry.Name)
	}
	next := entry.canonical()
	next.Provenance.ContentHash = HashBody(next.Body)
	return s.writeOver(stored, next, now)
}

// Delete tombstones a record.
//
// The tombstone is written BEFORE the record is removed. An interruption
// between the two steps leaves both files present, and a tombstone always
// wins over the record beside it, so the deletion is durable from the
// moment the tombstone lands. The reverse order would leave an
// interruption looking like a record that was never deleted at all.
func (s *FileStore) Delete(ctx context.Context, kind MemoryKind, name string) error {
	if err := ctx.Err(); err != nil {
		return cascade.Wrap(cascade.KindCanceled, err, "memory delete canceled")
	}
	if err := checkKey(kind, name); err != nil {
		return err
	}
	present, err := s.liveFileExists(kind, name)
	if err != nil {
		return err
	}
	if !present {
		return cascade.Wrapf(cascade.KindNotFound, ErrNoSuchEntry,
			"cannot delete absent memory record %s/%s", kind, name)
	}
	if err := s.fs.WriteAtomic(s.tombstonePath(kind, name), nil); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, ErrStoreIO,
			"writing tombstone for %s/%s: %v", kind, name, err)
	}
	if err := s.fs.Remove(s.entryPath(kind, name)); err != nil && !isNotExist(err) {
		return cascade.Wrapf(cascade.KindUnavailable, ErrStoreIO,
			"removing memory record %s/%s: %v", kind, name, err)
	}
	return nil
}
