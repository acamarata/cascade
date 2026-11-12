// Package consult provides task classification utilities.
package consult

import "errors"

// ClassConstraints defines the properties and permissions associated with a
// validated task class.
type ClassConstraints struct {
	Class          string
	ReadOnly       bool
	RequiresVerify bool
}

// ErrUnknownTaskClass is returned when a task class name cannot be recognized.
var ErrUnknownTaskClass = errors.New("D3543: unknown task class")

// ValidateWorkClass checks whether the provided task class name is recognized.
// It does not trim or normalize the input. Empty, whitespace-only, unknown or
// differently cased names return zero constraints and ErrUnknownTaskClass.
func ValidateWorkClass(name string) (ClassConstraints, error) {
	// No supported classes are registered in this iteration.
	return ClassConstraints{}, ErrUnknownTaskClass
}
