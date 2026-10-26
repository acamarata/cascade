//go:build windows

package update

// Purpose: the windows half of the self-update lock: a one-byte
//   LockFileEx range (a zero-length range would lock nothing), released
//   before the file is removed (an open file cannot be removed there).
// SPORT: internal/update acquireUpdateLock/ADDED.

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockRangeBytes is the locked byte range.
const lockRangeBytes = 1

// tryLockFile takes the exclusive lock without blocking; false means another
// holder has it.
func tryLockFile(f *os.File) (bool, error) {
	ol := new(windows.Overlapped)
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, lockRangeBytes, 0, ol)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		return false, nil
	}
	return false, err
}

// unlockFile releases the lock taken by tryLockFile.
func unlockFile(f *os.File) {
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, lockRangeBytes, 0, new(windows.Overlapped))
}

// releaseLockFile releases the lock, closes the file, then removes it; a
// removal that fails because a contender already opened it is harmless.
func releaseLockFile(f *os.File, path string) {
	unlockFile(f)
	_ = f.Close()
	_ = os.Remove(path)
}
