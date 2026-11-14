// Package background provides autonomous worker guards.
package background

import "errors"

// Mode represents the execution mode of a worker.
type Mode string

const (
	// ModeAutonomous indicates the worker runs without user interaction.
	ModeAutonomous Mode = "autonomous"
	// ModeInteractive indicates the worker may prompt the user.
	ModeInteractive Mode = "interactive"
)

// ErrM1889 is returned when Guard receives an unknown mode.
var ErrM1889 = errors.New("M1889: unknown mode")

// Guard intercepts calls that would produce visible prompts.
// In autonomous mode it suppresses the call and returns true with nil error.
// In interactive mode it executes the call and returns false with the call's result.
// For any other mode it returns an ErrM1889 error.
func Guard(m Mode, call func() error) (suppressed bool, err error) {
	switch m {
	case ModeAutonomous:
		return true, nil
	case ModeInteractive:
		err = call()
		return false, err
	case BoundaryModeNormal:
		return false, ErrM1889
	default:
		return false, ErrM1889
	}
}
