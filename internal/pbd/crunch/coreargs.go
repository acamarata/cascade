// Package crunch holds the argument and quota-authorization checks for crunch runs.
package crunch

import (
	"errors"
	"strings"
)

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
	var ca CoreArgs
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			return ca, ErrInvalidFlags
		}
		rest := strings.TrimPrefix(arg, "--")
		var name, val string
		if idx := strings.Index(rest, "="); idx >= 0 {
			name = rest[:idx]
			val = rest[idx+1:]
		} else {
			name = rest
			if i+1 >= len(args) {
				return ca, ErrInvalidFlags
			}
			val = args[i+1]
			i++
		}
		switch name {
		case "dir":
			ca.Dir = val
		case "verify":
			ca.Verify = val
		case "prompt":
			ca.Prompt = val
		case "scope":
			ca.Scope = append(ca.Scope, val)
		default:
			return ca, ErrInvalidFlags
		}
	}
	return ca, nil
}
