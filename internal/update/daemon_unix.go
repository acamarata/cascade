//go:build !windows

package update

// Purpose: the production daemon probes Drain uses on unix: the pidfile
//   status (daemon.Status) and a sender bound to the one hand-off signal
//   (daemon.UpgradeSignal, SIGUSR2).
// Constraints: no other signal can be sent through UpgradeSignaller; the
//   UpgradeManager relaunch itself is unchanged (internal/daemon).
// SPORT: internal/update DaemonStatus/ADDED, UpgradeSignaller/ADDED.

import (
	"context"
	"syscall"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/runtime"
)

// DaemonStatus reports whether the daemon recorded at pidPath is running.
func DaemonStatus(pidPath string, clock runtime.Clock) func(context.Context) (bool, int, error) {
	return func(ctx context.Context) (bool, int, error) {
		res, err := daemon.Status(ctx, daemon.StatusOptions{PIDPath: pidPath, Prober: daemon.NewProber(), Clock: clock})
		return res.Running, res.PID, err
	}
}

// UpgradeSignaller binds send to daemon.UpgradeSignal.
func UpgradeSignaller(send func(pid int, sig syscall.Signal) error) func(pid int) error {
	return func(pid int) error { return send(pid, daemon.UpgradeSignal) }
}

// ProductionSignal sends the hand-off signal with daemon.DefaultSignal.
func ProductionSignal() func(pid int) error { return UpgradeSignaller(daemon.DefaultSignal) }

// drainsDaemon reports whether Apply hands off to a running daemon here.
const drainsDaemon = true
