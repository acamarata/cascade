//go:build !windows

package update

// Purpose: the POSIX half of the self-update lock: a non-blocking exclusive
//   flock, released after the lock file is removed so a contender that
//   still holds the old file sees it is gone and retries.
// SPORT: internal/update acquireUpdateLock/ADDED.

import (
	"errors"
	"os"
	"syscall"
)

// tryLockFile takes the exclusive lock without blocking; false means another
// holder has it.
func tryLockFile(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		return false, nil
	}
	return false, err
}

// unlockFile releases the lock taken by tryLockFile.
func unlockFile(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

// releaseLockFile removes path while the lock is held, then releases it.
func releaseLockFile(f *os.File, path string) {
	_ = os.Remove(path)
	unlockFile(f)
	_ = f.Close()
}
