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
// The writable classes write, build-simple, build and build-hard return
// ReadOnly=false and RequiresVerify=true. The read-only classes check, extract,
// read-long, architect, review, review-deep and gate return ReadOnly=true and
// RequiresVerify=true. It does not trim or normalize the input. Empty,
// whitespace-only, unknown or differently cased names return zero constraints
// and ErrUnknownTaskClass.
func ValidateWorkClass(name string) (ClassConstraints, error) {
	switch name {
	case "write", "build-simple", "build", "build-hard":
		return ClassConstraints{
			Class:          name,
			ReadOnly:       false,
			RequiresVerify: true,
		}, nil
	case "check", "extract", "read-long", "architect", "review", "review-deep", "gate":
		return ClassConstraints{
			Class:          name,
			ReadOnly:       true,
			RequiresVerify: true,
		}, nil
	default:
		return ClassConstraints{}, ErrUnknownTaskClass
	}
}
