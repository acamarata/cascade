// Package write contains control-plane write-path validation helpers.
package write

import "fmt"

// PhaseGapError reports a phase-gap validation failure with a stable error
// code that callers can inspect.
type PhaseGapError struct {
	// Code is the stable error code for the validation failure.
	Code string

	// Supervisor is the supervisor identifier that failed validation.
	Supervisor string

	// Gap is the phase-gap value that was validated.
	Gap int
}

// Error implements the error interface.
func (e PhaseGapError) Error() string {
	return fmt.Sprintf("phase gap validation failed: supervisor %s gap %d: %s", e.Supervisor, e.Gap, e.Code)
}

const (
	supervisorP11 = "p11"
	supervisorP12 = "p12"

	phaseGapP11 = 5
	phaseGapP12 = 3

	errCodeUnknownSupervisor = "T0588"
)

// ValidatePhaseGap verifies that the supplied phase gap matches the decided
// value for the named supervisor. The merged flag is accepted for post-merge
// shadow verification, but the merged state does not change the rule.
//
// It returns true when the gap is valid for a known supervisor. It returns
// false and a nil error when the supervisor is known but the gap is invalid.
// It returns false and a PhaseGapError with code T0588 for an unknown
// supervisor.
func ValidatePhaseGap(sup string, gap int, merged bool) (bool, error) {
	// Post-merge validation preserves the same phase-gap semantics.
	_ = merged

	switch sup {
	case supervisorP11:
		return gap == phaseGapP11, nil
	case supervisorP12:
		return gap == phaseGapP12, nil
	default:
		return false, PhaseGapError{
			Code:       errCodeUnknownSupervisor,
			Supervisor: sup,
			Gap:        gap,
		}
	}
}
