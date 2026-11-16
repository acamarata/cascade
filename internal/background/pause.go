// Package background contains supervisor background behaviours.
package background

// autonomousMode is the mode name checked for supervisor pause-on-exit.
const autonomousMode = "Autonomous"

// PauseOnExit reports whether the supervisor must be paused when leaving the
// given mode. It returns true only when mode is Autonomous and active is
// false, so the supervisor stays paused until manually resumed.
func PauseOnExit(mode string, active bool) bool {
	return mode == autonomousMode && !active
}
