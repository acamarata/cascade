//go:build !windows

// Purpose: signal a whole process group on unix.
//
// Inputs: the group id and the signal.
// Outputs: nil when delivered or when the group is already gone (ESRCH);
//
//	KindInvalidInput for a pgid kill(2) would misread; KindUnavailable for
//	any other refusal (EPERM included).
//
// Constraints: a pgid of 1 or below, or above the 32-bit pid_t range, is
//
//	never passed to kill(2): 0 would target the caller's own group, -1
//	every process, and a value past MaxInt32 truncates to some other
//	target. A caller must never signal a group whose leader it has
//	already waited for, because that pgid can be reused (R134).
//
// SPORT: pkg/procgroup (ADD) — P1-PLG-09.

package procgroup

import (
	"errors"
	"math"
	"syscall"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Signal sends sig to every process in group pgid. ESRCH means the group
// has no members left, which is the outcome a kill wants, so it is nil.
func Signal(pgid int, sig syscall.Signal) error {
	if pgid <= 1 || pgid > math.MaxInt32 {
		return cascade.Newf(cascade.KindInvalidInput, "procgroup: refusing to signal process group %d", pgid)
	}
	if err := syscall.Kill(-pgid, sig); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return cascade.Wrapf(cascade.KindUnavailable, err, "procgroup: signalling process group %d with %v", pgid, sig)
	}
	return nil
}
