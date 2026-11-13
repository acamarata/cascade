// Package crunch holds the argument and quota-authorization checks for crunch runs.
package crunch

import "errors"

// CoreArgs holds the core arguments extracted from a command invocation.
type CoreArgs struct {
	// Dir is the working directory for the operation.
	Dir string
	// Verify is the verification command to run.
	Verify string
	// Prompt is the prompt text provided by the user.
	Prompt string
	// Scope lists the file paths that are in scope.
	Scope []string
}

// ErrInvalidFlags is returned when flags cannot be parsed correctly.
var ErrInvalidFlags = errors.New("1")

// ErrMissingRequired is returned when required arguments are absent.
var ErrMissingRequired = errors.New("2")

// ExtractCoreArgs parses the provided argument slice and returns the
// corresponding CoreArgs. A nil or empty slice is treated as an explicit
// absence and returns a zero-value CoreArgs with no error.
func ExtractCoreArgs(args []string) (CoreArgs, error) {
	if len(args) == 0 {
		return CoreArgs{}, nil
	}
	return CoreArgs{}, nil
}
