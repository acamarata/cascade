//go:build windows

// Purpose: the windows half of pkg/procgroup, and a plain statement of what
//
//	it cannot do.
//
// Inputs: as the unix half.
// Outputs: Attr is nil; Signal and Lookup always refuse with
//
//	KindUnsupported; Exists and Empty report false (nothing confirmed).
// Constraints: windows has no kill(-pgid). Reaping a whole tree there takes
//
//	a Job Object (internal/ci's runner_exec_windows.go builds one for its
//	own steps), which is a different shape from a signal. A caller on
//	windows kills the child it holds and reports that the tree may
//	survive, rather than this package pretending to signal a group.
//
// SPORT: pkg/procgroup (ADD) — P1-PLG-09; (CHANGE) — P1-AGT-01.

package procgroup

import (
	"syscall"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Attr returns nil on windows: there is no process-group attribute to set
// here (callers that want CREATE_NEW_PROCESS_GROUP set it themselves).
func Attr() *syscall.SysProcAttr { return nil }

// Signal refuses on windows; see this file's header.
func Signal(pgid int, sig syscall.Signal) error {
	return cascade.Newf(cascade.KindUnsupported,
		"procgroup: windows cannot signal process group %d with %v; kill the child and treat the tree as unreaped", pgid, sig)
}

// Lookup refuses on windows; there is no process group to look up.
func Lookup(pid int) (int, error) {
	return 0, cascade.Newf(cascade.KindUnsupported, "procgroup: windows has no process group for pid %d", pid)
}

// Exists reports false on windows: no group is ever created here.
func Exists(int) bool { return false }

// Empty reports false on windows: an empty group cannot be confirmed, so a
// caller never reads a tree as reaped on this platform's word.
func Empty(int) bool { return false }
