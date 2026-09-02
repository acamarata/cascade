//go:build darwin

// Purpose: darwin implementation of the §D-3 arbitration exclusive lock —
//   syscall.Flock(LOCK_EX|LOCK_NB) on a sidecar "<path>.lock" file (never
//   the .db file itself, which modernc-sqlite's own OS-level locking
//   already manages; layering a second app-level flock directly onto the
//   database file risks confusing SQLite's own lock state machine — see
//   providers/sqlite/README.md "Why a sidecar lock file").
// Constraints: build-tagged darwin-only per files_scope.
// SPORT: providers.sqlite.Driver/ADDED (P1-E02-W1-S02-T2).

package sqlite

import (
	"os"
	"syscall"

	"github.com/acamarata/cascade/pkg/cascade"
)

// acquireExclusiveLock opens (creating if absent) path+".lock" and takes a
// non-blocking exclusive flock on it. On success it returns an unlock func
// that releases the lock and closes the file descriptor; the returned
// error is non-nil (and unlock nil) if the lock is already held by another
// process (syscall.EWOULDBLOCK) or the sidecar file could not be opened.
func acquireExclusiveLock(path string) (unlock func() error, err error) {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "sqlite: open lock file for %s", path)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, cascade.Wrapf(cascade.KindConflict, err, "sqlite: exclusive lock held by another process on %s", path)
	}
	return func() error {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return f.Close()
	}, nil
}
