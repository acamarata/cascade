// Purpose: the typed signals and the no-such-process error type, so a caller
//
//	can signal and look up a group without naming syscall itself.
//
// Inputs: none.
// Outputs: Sig, SigTerm, SigKill, NoSuchProcessError and IsNoSuchProcess.
// Constraints: Sig is an alias of syscall.Signal, so Signal's signature and
//
//	every existing caller stay unchanged. Both signals exist on windows
//	too, which keeps a caller's windows build compiling; Signal refuses
//	there anyway. NoSuchProcessError is a distinct type, not a taxonomy
//	sentinel, so IsNoSuchProcess matches it by type and never by Kind
//	(errors.Is on two taxonomy errors compares Kind only); Lookup still
//	returns it wrapped in a KindNotFound taxonomy error.
//
// SPORT: pkg/procgroup (CHANGE) — P1-AGT-01.

package procgroup

import (
	"errors"
	"strconv"
	"syscall"
)

// Sig is the signal type Signal takes. Sig(0) probes a group without
// delivering anything.
type Sig = syscall.Signal

// SigTerm asks a group to exit; SigKill ends it.
const (
	SigTerm Sig = syscall.SIGTERM
	SigKill Sig = syscall.SIGKILL
)

// NoSuchProcessError is in the chain of a Lookup error when Pid no longer
// exists (ESRCH). A zombie can still report it on darwin, so a caller that
// needs the group confirms it with Exists.
type NoSuchProcessError struct{ Pid int }

// Error names the pid that no longer exists.
func (e *NoSuchProcessError) Error() string {
	return "procgroup: no such process " + strconv.Itoa(e.Pid)
}

// Unwrap exposes ESRCH, so errors.Is(err, syscall.ESRCH) still holds.
func (e *NoSuchProcessError) Unwrap() error { return syscall.ESRCH }

// IsNoSuchProcess reports whether err's chain holds a NoSuchProcessError.
func IsNoSuchProcess(err error) bool {
	var nsp *NoSuchProcessError
	return errors.As(err, &nsp)
}
