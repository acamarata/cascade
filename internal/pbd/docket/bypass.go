package docket

import "errors"

// ErrInvalidBypassInput is returned when a BypassInput contains invalid values.
var ErrInvalidBypassInput = errors.New("INVALID_BYPASS_INPUT")

// BypassInput represents the parameters for a bypass admission decision.
type BypassInput struct {
	Lane          string
	Hot           bool
	Cap           int
	OverflowCap   int
	Occupancy     int
	GateDeadlines int
	RunSeats      int
}

// BypassDecision represents the outcome of a bypass admission evaluation.
type BypassDecision struct {
	Admitted bool
	Bypassed bool
}

// Bypass evaluates a bypass admission request and returns a decision.
// It returns ErrInvalidBypassInput if the lane is not A3 or A2, or if any integer field is negative.
func Bypass(in BypassInput) (BypassDecision, error) {
	if in.Lane != "A3" && in.Lane != "A2" {
		return BypassDecision{}, ErrInvalidBypassInput
	}
	if in.Cap < 0 || in.OverflowCap < 0 || in.Occupancy < 0 || in.GateDeadlines < 0 || in.RunSeats < 0 {
		return BypassDecision{}, ErrInvalidBypassInput
	}

	admitted := in.Occupancy < in.Cap
	if !admitted && in.Cap == 0 && in.Hot {
		admitted = true
	}

	return BypassDecision{Admitted: admitted, Bypassed: false}, nil
}
