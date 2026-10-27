//go:build windows

// Purpose: the non-blocking exclusive file lock daemon-key provisioning
//
//	polls (LockFileEx: released when the handle closes or the process exits).
//
// SPORT: internal/secrets daemon-key lock (P1-SEC-23).

package secrets

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

const daemonKeyLockBytes = 1

func tryLockDaemonKey(f *os.File) (bool, error) {
	ol := new(windows.Overlapped)
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, daemonKeyLockBytes, 0, ol)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		return false, nil
	}
	return false, err
}

func unlockDaemonKey(f *os.File) {
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, daemonKeyLockBytes, 0, new(windows.Overlapped))
}
