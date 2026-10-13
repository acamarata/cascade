//go:build !windows

// Purpose: look up a process's group and test whether a group still has
//
//	members, on unix.
//
// Inputs: a pid or a pgid.
// Outputs: Lookup's pgid or typed error; Exists and Empty verdicts.
// Constraints: kill(-pgid, 0) splits three ways: nil (a member this
//
//	process may signal), EPERM (members exist, none signalable; on
//	darwin a group holding only an unreaped zombie answers this way) and
//	ESRCH (no member left). Exists counts the first two; Empty is true
//	only on ESRCH, so a probe error is never read as "empty". Lookup
//	refuses pid 0 or below, which getpgid(2) reads as the caller itself.
//	The same pgid range check as Signal applies throughout.
//
// SPORT: pkg/procgroup (CHANGE) — P1-AGT-01.

package procgroup

import (
	"errors"
	"math"
	"syscall"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Lookup returns the process group of pid. A pid that no longer exists
// returns a KindNotFound error whose chain holds a NoSuchProcessError; any
// other failure is KindUnavailable.
func Lookup(pid int) (int, error) {
	if pid <= 0 || pid > math.MaxInt32 {
		return 0, cascade.Newf(cascade.KindInvalidInput, "procgroup: refusing to look up pid %d", pid)
	}
	pgid, err := syscall.Getpgid(pid)
	switch {
	case err == nil:
		return pgid, nil
	case errors.Is(err, syscall.ESRCH):
		return 0, cascade.Wrapf(cascade.KindNotFound, &NoSuchProcessError{Pid: pid},
			"procgroup: looking up the group of pid %d", pid)
	}
	return 0, cascade.Wrapf(cascade.KindUnavailable, err, "procgroup: looking up the group of pid %d", pid)
}

// Exists reports whether group pgid still has a member, signalable or not.
func Exists(pgid int) bool {
	if !validPgid(pgid) {
		return false
	}
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Empty reports whether group pgid is confirmed to have no member left.
func Empty(pgid int) bool {
	return validPgid(pgid) && errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH)
}

// validPgid is the range kill(2) reads as exactly one group.
func validPgid(pgid int) bool {
	return pgid > 1 && pgid <= math.MaxInt32
}
