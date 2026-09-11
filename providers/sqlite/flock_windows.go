//go:build windows

// Purpose: windows implementation of the §D-3 arbitration exclusive lock —
//
//	LockFileEx(LOCKFILE_EXCLUSIVE_LOCK|LOCKFILE_FAIL_IMMEDIATELY) on a
//	sidecar "<path>.lock" file, mirroring flock_darwin.go's and
//	flock_linux.go's rationale for never locking the .db file itself:
//	Windows locks are mandatory (not advisory), so locking the .db would
//	block modernc-sqlite's own reads, not just contend with another
//	process's exclusive open.
//
// THREE TRAPS this implementation exists to avoid, each of which fails
// OPEN — worse than the tier-2 refusal it replaces:
//  1. LockFileEx called with a ZERO-length range SUCCEEDS AND LOCKS
//     NOTHING: two openers would both "acquire" and neither would ever
//     see the other. acquireExclusiveLock locks exactly lockRangeBytes (1)
//     byte at offset 0 (legal past EOF on Windows), and its returned
//     unlock func releases the IDENTICAL range — a mismatched range fails
//     to release.
//  2. Omitting LOCKFILE_FAIL_IMMEDIATELY makes LockFileEx BLOCK instead of
//     returning ERROR_LOCK_VIOLATION, which would turn the contention test
//     into a package-level timeout. Always OR it into the flags.
//  3. Locks are per-HANDLE and mandatory: the sidecar is opened with
//     FILE_SHARE_READ|FILE_SHARE_WRITE|FILE_SHARE_DELETE so a second
//     process can still open (not lock) the same file, and the returned
//     unlock func calls UnlockFileEx BEFORE windows.CloseHandle — closing
//     first would make t.TempDir's later cleanup fail on the still-locked
//     file.
//
// Constraints: build-tagged windows-only per files_scope. golang.org/x/sys
//
//	is already a DIRECT go.mod requirement (promoted for the linux flock
//	path) with an existing licenses.go entry — x/sys/windows is the same
//	module, so this file adds no new dependency and no new license entry.
//
// SPORT: providers.sqlite.Driver/CHANGED (windows LockFileEx ticket,
//
//	supersedes the P1-E02-W1-S02-T2 tier-2 refusal).

package sqlite

import (
	"errors"

	"golang.org/x/sys/windows"

	"github.com/acamarata/cascade/pkg/cascade"
)

// lockRangeBytes is the length of the byte range acquireExclusiveLock locks
// and unlocks — see trap 1 in this file's doc comment. It must never be 0.
const lockRangeBytes = 1

// acquireExclusiveLock opens (creating if absent) the canonicalized
// path+".lock" and takes a non-blocking, whole-process-exclusive
// LockFileEx byte-range lock on it, mirroring flock_darwin.go's and
// flock_linux.go's contract exactly: on success it returns an unlock func
// that releases the lock and closes the handle; the returned error is
// non-nil (and unlock nil) if the lock is already held by another process
// (windows.ERROR_LOCK_VIOLATION, reported as cascade.KindConflict wrapping
// ErrLockHeld) or the sidecar file could not be opened
// (cascade.KindUnavailable).
func acquireExclusiveLock(path string) (unlock func() error, err error) {
	resolved, err := canonicalDBPath(path)
	if err != nil {
		return nil, err
	}

	handle, err := openLockSidecar(resolved + ".lock")
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "sqlite: open lock file for %s", path)
	}

	overlapped := windows.Overlapped{}
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)
	if lockErr := windows.LockFileEx(handle, flags, 0, lockRangeBytes, 0, &overlapped); lockErr != nil {
		_ = windows.CloseHandle(handle)
		joined := errors.Join(lockErr, ErrLockHeld)
		return nil, cascade.Wrapf(cascade.KindConflict, joined, "sqlite: exclusive lock held by another process on %s", path)
	}

	return func() error {
		unlockOverlapped := windows.Overlapped{}
		unlockErr := windows.UnlockFileEx(handle, 0, lockRangeBytes, 0, &unlockOverlapped)
		closeErr := windows.CloseHandle(handle)
		if unlockErr != nil {
			return cascade.Wrapf(cascade.KindUnavailable, unlockErr, "sqlite: unlock %s", path)
		}
		if closeErr != nil {
			return cascade.Wrapf(cascade.KindUnavailable, closeErr, "sqlite: close lock handle for %s", path)
		}
		return nil
	}, nil
}

// openLockSidecar opens (creating if absent) the sidecar lock file with
// FILE_SHARE_READ|FILE_SHARE_WRITE|FILE_SHARE_DELETE so that opening the
// file from a second process never itself fails — only the byte-range
// LockFileEx call below contends, exactly matching flock_darwin.go's and
// flock_linux.go's os.OpenFile(os.O_CREATE|os.O_RDWR, 0o644), which permits
// any process to open the sidecar and only the flock call to contend.
func openLockSidecar(sidecarPath string) (windows.Handle, error) {
	namePtr, err := windows.UTF16PtrFromString(sidecarPath)
	if err != nil {
		return windows.InvalidHandle, err
	}
	shareMode := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE)
	handle, err := windows.CreateFile(
		namePtr,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		shareMode,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return handle, nil
}
