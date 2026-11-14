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

// ErrInvalidFlags is returned when flags cannot be parsed correctly: an
// unknown flag, a positional token, a bare "--", a repeated scalar flag or a
// duplicate scope path. Its Error text is the literal code "1".
var ErrInvalidFlags = errors.New("1")

// ErrMissingRequired is returned when a flag value is missing or blank, or
// when a nonempty invocation lacks --dir or --prompt. Its Error text is the
// literal code "2".
var ErrMissingRequired = errors.New("2")

// ExtractCoreArgs parses the provided argument slice and returns the
// corresponding CoreArgs. A nil or empty slice is treated as an explicit
// absence and returns a zero-value CoreArgs with no error. Flags take the
// forms "--name value" and "--name=value". The first syntax or value error
// in argument order is returned; required flags are checked after the scan.
// Every failure returns the zero CoreArgs, and args is never modified.
func ExtractCoreArgs(args []string) (CoreArgs, error) {
	if len(args) == 0 {
		return CoreArgs{}, nil
	}
	var ca CoreArgs
	seen := map[string]bool{}
	scopes := map[string]bool{}
	for i := 0; i < len(args); i++ {
		name, val, used, err := coreArgsNext(args, i)
		if err != nil {
			return CoreArgs{}, err
		}
		i += used
		if name == "scope" {
			if scopes[val] {
				return CoreArgs{}, ErrInvalidFlags
			}
			scopes[val] = true
			ca.Scope = append(ca.Scope, val)
			continue
		}
		if seen[name] {
			return CoreArgs{}, ErrInvalidFlags
		}
		seen[name] = true
		switch name {
		case "dir":
			ca.Dir = val
		case "verify":
			ca.Verify = val
		case "prompt":
			ca.Prompt = val
		}
	}
	if !seen["dir"] || !seen["prompt"] {
		return CoreArgs{}, ErrMissingRequired
	}
	return ca, nil
}

// coreArgsNext reads the flag at args[i] and returns its name, its value and
// how many extra tokens the value consumed (0 for "--name=value", 1 for
// "--name value"). It reports ErrInvalidFlags for a positional token, a bare
// "--" or an unknown name, and ErrMissingRequired for an absent or blank
// value, including a following token that begins with "--".
func coreArgsNext(args []string, i int) (string, string, int, error) {
	arg := args[i]
	if !strings.HasPrefix(arg, "--") || arg == "--" {
		return "", "", 0, ErrInvalidFlags
	}
	rest := arg[2:]
	name, val, hasEq := strings.Cut(rest, "=")
	switch name {
	case "dir", "verify", "prompt", "scope":
	default:
		return "", "", 0, ErrInvalidFlags
	}
	used := 0
	if !hasEq {
		if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
			return "", "", 0, ErrMissingRequired
		}
		val = args[i+1]
		used = 1
	}
	if strings.TrimSpace(val) == "" {
		return "", "", 0, ErrMissingRequired
	}
	return name, val, used, nil
}
