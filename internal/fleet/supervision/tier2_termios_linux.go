//go:build linux

// Purpose: the two termios ioctl request numbers on linux. See
//
//	tier2_termios_darwin.go for why these are per-platform constants
//	rather than a runtime switch.
//
// SPORT: fleet.supervision.Tier2Supervisor/ADDED (P1-E18-W4-S39-T3).

package supervision

import "golang.org/x/sys/unix"

const (
	echoGetRequest = unix.TCGETS
	echoSetRequest = unix.TCSETS
)
