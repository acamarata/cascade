//go:build windows

package update

// Purpose: the windows half of the daemon probes. The daemon does not run
//   on windows (daemon.Status refuses), so Apply never drains there: it
//   renames the old files aside and the next start runs the new ones.
// SPORT: internal/update DaemonStatus/ADDED.

import (
	"context"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// DaemonStatus reports no running daemon.
func DaemonStatus(string, runtime.Clock) func(context.Context) (bool, int, error) {
	return func(context.Context) (bool, int, error) { return false, 0, nil }
}

// ProductionSignal refuses: there is no daemon to hand off to.
func ProductionSignal() func(pid int) error {
	return func(int) error {
		return cascade.New(cascade.KindUnsupported, "update: the daemon is not supported on this platform")
	}
}

// drainsDaemon reports whether Apply hands off to a running daemon here.
const drainsDaemon = false
