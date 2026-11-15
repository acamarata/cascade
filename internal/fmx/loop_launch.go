package fmx

import "errors"

// LaunchSpec specifies how to launch a loop command.
type LaunchSpec struct {
	// Command is the command and arguments to run.
	Command []string
	// OuterSandbox indicates whether the launch should occur within an outer sandbox.
	OuterSandbox bool
}

// ErrNestedSandbox is returned when a sandbox is requested inside an existing sandbox.
var ErrNestedSandbox = errors.New("fmx: nested sandbox")

// ErrInvalidLoopCommand is returned when the loop command is missing or invalid.
var ErrInvalidLoopCommand = errors.New("fmx: invalid loop command")

// BuildLoopLaunch constructs the argument list for launching a loop.
// It returns a fresh copy of the command slice and never mutates the caller's slice.
// It fails closed if the command is empty or nil.
func BuildLoopLaunch(spec LaunchSpec) ([]string, error) {
	if len(spec.Command) == 0 {
		return nil, ErrInvalidLoopCommand
	}

	res := make([]string, len(spec.Command))
	copy(res, spec.Command)

	return res, nil
}
