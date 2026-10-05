//go:build windows

// Purpose: the windows half of pkg/procgroup, and a plain statement of what
//
//	it cannot do.
//
// Inputs: as the unix half.
// Outputs: Attr is nil; Signal always refuses with KindUnsupported.
// Constraints: windows has no kill(-pgid). Reaping a whole tree there takes
//
//	a Job Object (internal/ci's runner_exec_windows.go builds one for its
//	own steps), which is a different shape from a signal. A caller on
//	windows kills the child it holds and reports that the tree may
//	survive, rather than this package pretending to signal a group.
//
// SPORT: pkg/procgroup (ADD) — P1-PLG-09.

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
