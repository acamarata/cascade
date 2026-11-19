// Package background provides operational controls for background tasks.
package background

import "errors"

// State represents the operational state of the brake.
type State string

const (
	// StateRunning indicates that operations are active.
	StateRunning State = "running"
	// StateBraked indicates that operations are halted.
	StateBraked State = "braked"
)

var (
	// ErrUnauthorized is returned when an owner action is invalid.
	ErrUnauthorized = errors.New("unauthorized")
	// ErrHalted is returned when operations are attempted while braked.
	ErrHalted = errors.New("halted")
)

// Brake controls operational flow based on budget and owner authorization.
// It extends the pause/resume state per P2-BGA-69 and M1892.
type Brake struct {
	// State holds the current operational state.
	State State
}

// Check evaluates the budget and triggers a hard brake if exhausted.
// Budget exhaustion acts as a hard brake requiring explicit owner resume.
func (b *Brake) Check(budget int) {
	if budget <= 0 {
		b.State = StateBraked
	}
}

// Resume clears the brake if the owner provides the correct action.
func (b *Brake) Resume(ownerAction string) error {
	if ownerAction != "resume" {
		return ErrUnauthorized
	}
	b.State = StateRunning
	return nil
}

// Do returns an error if operations are attempted while the brake is engaged.
func (b *Brake) Do() error {
	if b.State == StateBraked {
		return ErrHalted
	}
	return nil
}
