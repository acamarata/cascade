package boundaries

import (
	"errors"
	"strings"
)

// ErrInvalidSupervisor is returned when a method is called on a nil Supervisor.
var ErrInvalidSupervisor = errors.New("invalid supervisor")

// State holds the execution state of a Supervisor, including skipped boundary steps.
type State struct {
	Skipped []string
}

// Supervisor manages the execution of steps and tracks skipped boundary steps.
type Supervisor struct {
	State State
}

// Run processes a list of steps. Any step starting with "BOUNDARY:" is skipped
// and recorded verbatim in State.Skipped. Non-boundary steps are ignored.
// Returns ErrInvalidSupervisor if the receiver is nil.
func (s *Supervisor) Run(steps []string) error {
	if s == nil {
		return ErrInvalidSupervisor
	}
	for _, step := range steps {
		if strings.HasPrefix(step, "BOUNDARY:") {
			s.State.Skipped = append(s.State.Skipped, step)
		}
	}
	return nil
}
