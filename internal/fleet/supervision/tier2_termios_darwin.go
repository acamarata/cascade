//go:build darwin

// Purpose: the two termios ioctl request numbers, which differ between
//
//	darwin and linux. Split per platform rather than switched at runtime
//	because they are compile-time constants on each and a runtime switch
//	would be a branch that is always wrong on one of them.
//
// SPORT: fleet.supervision.Tier2Supervisor/ADDED (P1-E18-W4-S39-T3).

package supervision

import "golang.org/x/sys/unix"

const (
	echoGetRequest = unix.TIOCGETA
	echoSetRequest = unix.TIOCSETA
)
