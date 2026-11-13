package statesave

import "errors"

// State represents the current state of the background process.
type State string

const (
	// Running indicates the process is active.
	Running State = "running"
	// Halted indicates the process is stopped.
	Halted State = "halted"
)

// Question represents a user prompt related to the save operation.
type Question struct {
	// Prompt is the message displayed to the user.
	Prompt string
}

// ErrInvalidSave is the error returned when the save callback is nil.
var ErrInvalidSave = errors.New("nil save callback")

// Save runs the provided save callback and returns the resulting state and question.
// On success, it returns Running and nil.
func Save(save func() error) (State, *Question) {
	if save == nil {
		return Halted, &Question{Prompt: ErrInvalidSave.Error()}
	}
	err := save()
	if err != nil {
		return Halted, &Question{Prompt: err.Error()}
	}
	return Running, nil
}
