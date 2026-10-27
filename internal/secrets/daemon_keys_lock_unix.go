//go:build !windows

// Purpose: the non-blocking exclusive file lock daemon-key provisioning
//
//	polls (flock: per open file, released when the process exits).
//
// SPORT: internal/secrets daemon-key lock (P1-SEC-23).

package secrets

import (
	"errors"
	"os"
	"syscall"
)

func tryLockDaemonKey(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		return false, nil
	}
	return false, err
}

func unlockDaemonKey(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
