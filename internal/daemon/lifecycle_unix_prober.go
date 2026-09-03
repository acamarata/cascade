//go:build !windows

package daemon

// Purpose: unixProber — the production ProcessProber (daemon.go) for
//   non-Windows platforms: IsAlive via a signal-0 probe (the standard
//   liveness check that sends no real signal), StartTime by shelling out to
//   `ps -o lstart=` and parsing its output. Split from lifecycle_unix.go
//   under R-14.117 (Art.10.3's 300-line file cap).
// Inputs: a PID.
// Outputs: liveness (bool) and, best-effort, the process's actual start
//   time — used by classifyPID (daemon.go) to tell a live daemon apart
//   from a DIFFERENT process that has since reused the same PID.
// Constraints: `ps -o lstart=` is the same STANDARD format specifier on
//   both darwin and linux (verified: both ps implementations accept it),
//   so one implementation covers both release platforms without a
//   per-kernel branch. Its resolution is whole seconds, hence daemon.go's
//   recycleTolerance.
// SPORT: internal/daemon (ADD, per T-2 sport_updates).

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// unixProber is the zero-value-usable production ProcessProber.
type unixProber struct{}

// NewProber returns the production ProcessProber for this platform, for
// cmd/cascade's composition root (Art.10.2) to inject.
func NewProber() ProcessProber { return unixProber{} }

// IsAlive sends signal 0 to pid: this delivers no actual signal, but the
// kernel still performs the permission/existence check, so a nil error
// means a live process the caller has rights to signal.
func (unixProber) IsAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// StartTime shells out to `ps -o lstart= -p <pid>` and parses the result.
// ok is false when the process is gone or the output cannot be parsed —
// callers must treat that as "unknown", never as a zero-value start time.
func (unixProber) StartTime(pid int) (time.Time, bool) {
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return time.Time{}, false
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return time.Time{}, false
	}
	// ps's lstart format: "Mon Jan  2 15:04:05 2006", printed in the
	// process's local timezone — parsed with ParseInLocation(time.Local),
	// never the bare Parse (which would default to UTC and misreport the
	// delta by the local UTC offset on every non-UTC system).
	t, err := time.ParseInLocation("Mon Jan  2 15:04:05 2006", line, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
