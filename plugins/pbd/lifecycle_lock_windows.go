//go:build windows

// Package pbd (lifecycle_lock_windows.go): windows implementation of
// AppendIf's exclusive lock — LockFileEx(LOCKFILE_EXCLUSIVE_LOCK), no
// LOCKFILE_FAIL_IMMEDIATELY: a BLOCKING byte-range lock, so a contending
// caller queues instead of failing, mirroring lifecycle_lock_unix.go's
// blocking contract exactly (this is the opposite tradeoff from
// providers/sqlite/flock_windows.go, which fails immediately by design —
// see that file's own doc comment; a different, non-importable package,
// Art.10.2). LockFileEx never accepts a zero-length range (see
// flock_windows.go's trap 1): lockRangeBytes below must never be 0.
// Constraints: build-tagged windows per write_scope.
// SPORT: plugins/pbd lifecycle_appendif (ADD) — P1-PBD-07.
package pbd

import (
	"golang.org/x/sys/windows"

	"github.com/acamarata/cascade/pkg/cascade"
)

// lockRangeBytes is the length of the byte range acquireJournalLock locks
// and unlocks. It must never be 0 (a zero-length LockFileEx range locks
// nothing and always "succeeds").
const lockRangeBytes = 1

// acquireJournalLock opens (creating if absent) path and blocks until it
// holds an exclusive byte-range lock on it, returning an unlock func that
// releases the lock (UnlockFileEx) before closing the handle — releasing
// after close would leave the file locked for any later opener, including
// t.TempDir's own cleanup.
func acquireJournalLock(path string) (unlock func() error, err error) {
	namePtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindInternal, err, "pbd: lock path %q", path)
	}
	shareMode := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE)
	handle, err := windows.CreateFile(namePtr, windows.GENERIC_READ|windows.GENERIC_WRITE, shareMode, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindInternal, err, "pbd: opening lock file %q", path)
	}
	overlapped := windows.Overlapped{}
	if lerr := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, lockRangeBytes, 0, &overlapped); lerr != nil {
		_ = windows.CloseHandle(handle)
		return nil, cascade.Wrapf(cascade.KindInternal, lerr, "pbd: locking %q", path)
	}
	return func() error {
		unlockOverlapped := windows.Overlapped{}
		uerr := windows.UnlockFileEx(handle, 0, lockRangeBytes, 0, &unlockOverlapped)
		cerr := windows.CloseHandle(handle)
		if uerr != nil {
			return cascade.Wrapf(cascade.KindInternal, uerr, "pbd: unlock %q", path)
		}
		if cerr != nil {
			return cascade.Wrapf(cascade.KindInternal, cerr, "pbd: close lock handle for %q", path)
		}
		return nil
	}, nil
}
