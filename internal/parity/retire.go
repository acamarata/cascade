// Package parity provides cutover gate logic for cascade.
package parity

import "errors"

// ErrGateRed is returned when a retire plan is requested for a non-green verdict.
var ErrGateRed = errors.New("gate is red")

// Verdict represents the result of a parity gate check.
type Verdict struct {
	// Green indicates whether the gate passed.
	Green bool
}

// Step describes a single ordered action in a retire plan.
type Step struct {
	// Order is the 1-based sequence number of this step.
	Order int
	// Action is the human-readable description of the step.
	Action string
}

// RetirePlan returns the ordered retirement steps for a green verdict.
// A non-green verdict returns ErrGateRed and a nil slice.
// The function is pure: it returns data and performs no I/O.
func RetirePlan(v Verdict) ([]Step, error) {
	if !v.Green {
		return nil, ErrGateRed
	}
	return []Step{
		{Order: 1, Action: "freeze capp writes"},
		{Order: 2, Action: "verify the data export"},
		{Order: 3, Action: "remove the capp shim references"},
		{Order: 4, Action: "archive the capp tree"},
	}, nil
}
