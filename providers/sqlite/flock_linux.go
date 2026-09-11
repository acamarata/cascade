//go:build linux

// Purpose: linux implementation of the §D-3 arbitration exclusive lock —
//   golang.org/x/sys/unix.Flock(LOCK_EX|LOCK_NB) on a sidecar
//   "<path>.lock" file, mirroring flock_darwin.go's rationale for never
//   flocking the .db file itself.
// Constraints: build-tagged linux-only per files_scope.
// SPORT: providers.sqlite.Driver/ADDED (P1-E02-W1-S02-T2).

package sqlite

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"

	"github.com/acamarata/cascade/pkg/cascade"
)

// acquireExclusiveLock opens (creating if absent) the canonicalized
// path+".lock" (see lockpath.go's canonicalDBPath — this is what makes two
// different spellings of the same database file contend on the same lock)
// and takes a non-blocking exclusive flock on it. On success it returns an
// unlock func that releases the lock and closes the file descriptor; the
// returned error is non-nil (and unlock nil) if the lock is already held
// by another process (unix.EWOULDBLOCK) or the sidecar file could not be
// opened.
func acquireExclusiveLock(path string) (unlock func() error, err error) {
	resolved, err := canonicalDBPath(path)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(resolved+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "sqlite: open lock file for %s", path)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		// errors.Join, not a substitution: callers keep the STRONGER
		// errors.Is(err, unix.EWOULDBLOCK) check (lock_unix_errno_test.go)
		// as well as the new portable errors.Is(err, ErrLockHeld) one.
		joined := errors.Join(err, ErrLockHeld)
		return nil, cascade.Wrapf(cascade.KindConflict, joined, "sqlite: exclusive lock held by another process on %s", path)
	}
	return func() error {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		return f.Close()
	}, nil
}
