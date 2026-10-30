// Purpose: the file-backed PendingClearStore at
//
//	<data_dir>/secrets/pending-clears.json: the restart-surviving list of
//	clipboard writes whose clear has not been confirmed.
//
// Inputs: an absolute file path.
// Outputs: NewFilePendingClearStore, ErrPendingClearsCorrupt.
//
// Constraints: directory 0700, file 0600, every write through
//
//	runtime.WriteFileAtomic. A file that is not a regular file, does not
//	decode exactly, names an unknown version or holds an invalid row refuses
//	every call: a corrupt file is never read as empty. Put reads its row
//	back before returning, so a row lost to a concurrent writer refuses the
//	write instead of leaving a clipboard value with no recovery row. Calls
//	are serialised in-process; across processes the read-back is the guard
//	(docs/security-posture/clipboard.md).
//
// SPORT: internal/secrets NewFilePendingClearStore/ADDED (P1-SEC-09).

package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// PendingClear is the durable record of one clipboard write awaiting its
// clear: no payload, hash or byte length.
type PendingClear struct {
	RefID      string    `json:"ref_id"`
	Platform   string    `json:"platform"`
	WrittenAt  time.Time `json:"written_at"`
	ClearDueAt time.Time `json:"clear_due_at"`
}

// PendingClearStore persists PendingClear rows. Delete of an absent row
// succeeds.
type PendingClearStore interface {
	Put(ctx context.Context, pc PendingClear) error
	List(ctx context.Context) ([]PendingClear, error)
	Delete(ctx context.Context, refID string) error
}

const pendingClearsVersion = 1

// ErrPendingClearsCorrupt refuses a pending-clears file that cannot be
// trusted. Nothing is written to the clipboard while it stands.
var ErrPendingClearsCorrupt = cascade.New(cascade.KindIntegrity,
	"secrets: the pending clipboard clears file is corrupt; refusing (inspect or remove it, then clear the clipboard by hand)")

type pendingClearsFile struct {
	Version int            `json:"version"`
	Rows    []PendingClear `json:"rows"`
}

type filePendingClearStore struct {
	mu   sync.Mutex
	path string
}

// NewFilePendingClearStore returns the store over path. A path that is not
// absolute makes every call refuse.
func NewFilePendingClearStore(path string) PendingClearStore {
	return &filePendingClearStore{path: path}
}

// Put adds pc and confirms it is on disk.
func (s *filePendingClearStore) Put(_ context.Context, pc PendingClear) error {
	if err := validPendingClear(pc); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.load()
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.RefID == pc.RefID {
			return cascade.New(cascade.KindConflict, "secrets: a pending clear with this ref already exists")
		}
	}
	if err := s.save(append(rows, pc)); err != nil {
		return err
	}
	back, err := s.load()
	if err != nil {
		return err
	}
	for _, r := range back {
		if r.RefID == pc.RefID {
			return nil
		}
	}
	return cascade.New(cascade.KindConflict, "secrets: the pending clear was lost to a concurrent writer; nothing was written")
}

// List returns every pending clear.
func (s *filePendingClearStore) List(context.Context) ([]PendingClear, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// Delete removes refID; an absent row succeeds.
func (s *filePendingClearStore) Delete(_ context.Context, refID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.load()
	if err != nil {
		return err
	}
	kept := rows[:0:0]
	for _, r := range rows {
		if r.RefID != refID {
			kept = append(kept, r)
		}
	}
	if len(kept) == len(rows) {
		return nil
	}
	return s.save(kept)
}

func (s *filePendingClearStore) checkPath() error {
	if s.path == "" || !filepath.IsAbs(s.path) {
		return cascade.New(cascade.KindInvalidInput, "secrets: the pending clipboard clears path must be absolute")
	}
	return nil
}

func (s *filePendingClearStore) load() ([]PendingClear, error) {
	if err := s.checkPath(); err != nil {
		return nil, err
	}
	info, err := os.Lstat(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "secrets: could not read the pending clipboard clears")
	}
	if !info.Mode().IsRegular() {
		return nil, cascade.Wrap(cascade.KindIntegrity, ErrPendingClearsCorrupt, "secrets: the pending clears path is not a regular file")
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "secrets: could not read the pending clipboard clears")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var f pendingClearsFile
	if err := dec.Decode(&f); err != nil || dec.More() || f.Version != pendingClearsVersion {
		return nil, ErrPendingClearsCorrupt
	}
	for _, r := range f.Rows {
		if validPendingClear(r) != nil {
			return nil, ErrPendingClearsCorrupt
		}
	}
	return f.Rows, nil
}

func (s *filePendingClearStore) save(rows []PendingClear) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "secrets: could not create the pending clipboard clears directory")
	}
	if rows == nil {
		rows = []PendingClear{}
	}
	raw, err := json.Marshal(pendingClearsFile{Version: pendingClearsVersion, Rows: rows})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "secrets: could not encode the pending clipboard clears")
	}
	if err := runtime.WriteFileAtomic(s.path, raw, 0o600); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "secrets: could not write the pending clipboard clears")
	}
	return nil
}

// validPendingClear accepts a row whose ref is exactly what Write mints (a
// cascade.ID: 26 Crockford base32 characters) and whose times are set.
func validPendingClear(pc PendingClear) error {
	if !cascade.ID(pc.RefID).Valid() || pc.ClearDueAt.IsZero() || pc.WrittenAt.IsZero() {
		return cascade.Wrap(cascade.KindInvalidInput, ErrPendingClearsCorrupt, "secrets: a pending clear row is invalid")
	}
	return nil
}
