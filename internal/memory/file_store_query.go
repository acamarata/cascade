package memory

// Purpose: the read-only half of FileStore: existence checks and listing.
//   Split from file_store.go per the 300-line file cap.
// Inputs: a kind and, for existence, a record name.
// Outputs: a presence answer or a lexically ordered name list, and typed
//   pkg/cascade errors.
// Constraints: neither operation parses a record, so a single damaged file
//   can never make a kind unlistable or an unrelated record invisible.
// SPORT: G/memory-store (ADD, placeholder per T-1 sport_updates).

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// liveFileExists reports whether a non-tombstoned record file is present.
// It deliberately does not parse the file: a damaged record must still be
// deletable, which is the only way out of a file this build cannot read.
func (s *FileStore) liveFileExists(kind MemoryKind, name string) (bool, error) {
	tombstoned, err := s.fs.Exists(s.tombstonePath(kind, name))
	if err != nil {
		return false, cascade.Wrapf(cascade.KindUnavailable, ErrStoreIO,
			"checking tombstone for %s/%s: %v", kind, name, err)
	}
	if tombstoned {
		return false, nil
	}
	present, err := s.fs.Exists(s.entryPath(kind, name))
	if err != nil {
		return false, cascade.Wrapf(cascade.KindUnavailable, ErrStoreIO,
			"checking memory record %s/%s: %v", kind, name, err)
	}
	return present, nil
}

// Exists reports whether a live record is stored under kind and name.
func (s *FileStore) Exists(ctx context.Context, kind MemoryKind, name string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, cascade.Wrap(cascade.KindCanceled, err, "memory exists check canceled")
	}
	if err := checkKey(kind, name); err != nil {
		return false, err
	}
	return s.liveFileExists(kind, name)
}

// List returns the names of every live record of a kind, lexically
// ordered. It reads directory names only and parses nothing, so a single
// damaged record cannot make the rest of the kind unlistable.
func (s *FileStore) List(ctx context.Context, kind MemoryKind) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindCanceled, err, "memory list canceled")
	}
	if !kind.Valid() {
		return nil, cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidKind,
			"unknown memory kind %q", string(kind))
	}
	names, err := s.fs.ReadDirNames(filepath.Join(s.base, string(kind)))
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, ErrStoreIO,
			"listing memory kind %s: %v", kind, err)
	}
	return liveNames(names), nil
}

// liveNames turns a directory listing into the sorted set of record names
// that are not tombstoned.
func liveNames(names []string) []string {
	dead := make(map[string]bool)
	for _, n := range names {
		if stem, ok := strings.CutSuffix(n, tombstoneSuffix); ok {
			dead[stem] = true
		}
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		stem, ok := strings.CutSuffix(n, entrySuffix)
		if !ok || dead[stem] || ValidateName(stem) != nil {
			continue
		}
		out = append(out, stem)
	}
	sort.Strings(out)
	return out
}
