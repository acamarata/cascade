// Purpose: the exported read-only store open for diagnostics (doctor's audit
//
//	chain check), split from daemonless.go for the 300-line cap.
//
// Inputs: the cascade.db path. Outputs: a provider.Store that refuses writes.
// Constraints: no flock, no DDL, no migrator, no file created or changed.
//
// SPORT: internal/runtime OpenReadOnlyStore/ADDED (P1-SEC-12).

package runtime

import (
	"context"
	"errors"
	"os"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// StoreInTransitionError reports a store that cannot be read yet without a
// write: exactly one of the -wal and -shm files present (SQLite creates and
// removes the pair one file at a time, so a live writer that is starting or
// stopping shows this briefly, and a crashed writer leaves it for good), or a
// database file with no schema yet (a writer is creating it). The caller
// should report "try again", not "tampered". It is its own type because
// cascade errors compare by Kind, so a sentinel would match every conflict.
type StoreInTransitionError struct{ Path, Why string }

func (e *StoreInTransitionError) Error() string {
	return "runtime: read-only store " + e.Path + ": " + e.Why
}

// IsStoreInTransition reports whether err is, or wraps, that refusal.
func IsStoreInTransition(err error) bool {
	var target *StoreInTransitionError
	return errors.As(err, &target)
}

// OpenReadOnlyStore opens the store at path for reading only (doctor's
// audit-chain check uses it). A missing database is an error; the caller
// decides whether absence is healthy. The returned func closes the store.
//
// mode=ro on a WAL database whose writer closed cleanly creates -wal and
// -shm and cannot remove them, so with neither present it opens immutable=1
// (no lock, no write; with no -wal the main file is the whole state). With
// both present (a writer holds the store) it opens mode=ro. Exactly one
// present is a StoreInTransitionError (an empty lone -wal counts as absent), and so
// is a database with no tables at all; one with other tables but no kv is an
// integrity error, never a "try again".
func OpenReadOnlyStore(ctx context.Context, path string) (provider.Store, func() error, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, nil, cascade.Wrapf(cascade.KindNotFound, err, "runtime: read-only store %s", path)
	}
	mode := "mode=ro"
	wal, werr := os.Stat(path + "-wal")
	_, serr := os.Stat(path + "-shm")
	if werr == nil && serr != nil && wal.Size() == 0 {
		werr = os.ErrNotExist // an empty lone -wal holds no frames: the main file is the whole state
	}
	switch {
	case werr != nil && serr != nil:
		mode = "immutable=1"
	case (werr == nil) != (serr == nil):
		return nil, nil, &StoreInTransitionError{Path: path, Why: "the write-ahead log files are changing (a writer is starting, stopping or crashed)"}
	}
	ro, err := openReadOnlyStore(ctx, path, mode)
	if err != nil {
		return nil, nil, err
	}
	var kv, tables int
	row := ro.db.QueryRowContext(ctx, `SELECT count(CASE WHEN name = 'kv' THEN 1 END), count(*) FROM sqlite_master WHERE type = 'table'`)
	if err := row.Scan(&kv, &tables); err != nil {
		_ = ro.close()
		return nil, nil, cascade.Wrapf(cascade.KindUnavailable, err, "runtime: read-only store %s: reading the schema", path)
	}
	if kv == 0 && tables == 0 {
		_ = ro.close()
		return nil, nil, &StoreInTransitionError{Path: path, Why: "the store has no schema yet (a writer is creating it)"}
	}
	if kv == 0 {
		_ = ro.close()
		return nil, nil, cascade.New(cascade.KindIntegrity, "runtime: read-only store "+path+" has tables but no kv table: not a cascade store, or its log was dropped")
	}
	return ro, ro.close, nil
}
