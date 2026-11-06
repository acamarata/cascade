//go:build !windows

package nodes

import (
	"errors"
	"os"
	"syscall"
)

// tryLockRecordFile acquires an exclusive, non-blocking OS file lock.
func tryLockRecordFile(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		return false, nil
	}
	return false, err
}

func unlockRecordFile(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
