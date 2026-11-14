package write

import (
	"errors"
	"strings"
)

// ErrInvalidPhase is returned when the phase argument is empty or contains only whitespace.
var ErrInvalidPhase = errors.New("D1086: invalid phase")

// ErrPhaseStoreUnavailable is returned when the store argument is nil.
var ErrPhaseStoreUnavailable = errors.New("D1086: phase store unavailable")

// ErrInvalidWorkCount is returned when the store returns a negative work count.
var ErrInvalidWorkCount = errors.New("D1086: invalid work count")

// PhaseWorkStore defines the dependency for querying pending tasks in a phase.
type PhaseWorkStore interface {
	PendingTasks(phase string) (int, error)
}

// Report holds the outcome of an idle work check for a specific phase.
type Report struct {
	Phase    string
	WorkLeft int
}

// CheckPhaseIdleWorkLeft verifies whether a phase has pending work.
// It returns a report, a crunchCreated flag (always false), and any error encountered.
func CheckPhaseIdleWorkLeft(s PhaseWorkStore, phase string) (*Report, bool, error) {
	if strings.TrimSpace(phase) == "" {
		return nil, false, ErrInvalidPhase
	}
	if s == nil {
		return nil, false, ErrPhaseStoreUnavailable
	}

	count, err := s.PendingTasks(phase)
	if err != nil {
		return nil, false, err
	}
	if count < 0 {
		return nil, false, ErrInvalidWorkCount
	}

	return &Report{
		Phase:    phase,
		WorkLeft: count,
	}, false, nil
}
