//go:build windows

// Purpose: the windows twin of driver_unix.go. Spawn refuses with
//   ErrPlatform on windows before any of these run (platformRefusal), so
//   each body refuses rather than pretends.
// Inputs/Outputs: the same signatures as driver_unix.go, so driver.go and
//   exec.go compile unchanged under GOOS=windows.
// Constraints: SCOPE DEVIATION (declared): see driver_unix.go.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2), deviation.

package claude

import (
	"os/exec"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/procgroup"
)

// openNoFollow and openNonBlock have no windows equivalent; the windows
// build never spawns a job, so it never writes or scans a tree.
const (
	openNoFollow = 0
	openNonBlock = 0
)

func setProcessGroup(*exec.Cmd) {}

func resolvePgid(int) (int, error) {
	return 0, cascade.New(cascade.KindUnsupported, "claude: process groups are not supported on windows")
}

func signalGroup(int, procgroup.Sig) error {
	return cascade.New(cascade.KindUnsupported, "claude: process-group signals are not supported on windows")
}

// groupIsEmpty reports true: no windows process group is ever created.
func groupIsEmpty(int) bool { return true }

// auditStragglers reports a leak: the windows build cannot inspect one.
func auditStragglers(int, []string) bool { return true }
