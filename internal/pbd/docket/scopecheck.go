// Package docket provides ticket status transition checks.
package docket

import (
	"errors"
	"fmt"
	"strings"
)

// DiffProvider supplies the list of changed files for a ticket scope check.
type DiffProvider interface {
	// ChangedFiles returns the list of file paths that have been modified.
	ChangedFiles() ([]string, error)
}

// CodeError represents a domain error identified by a specific code.
type CodeError struct {
	// Code is the unique identifier for the error condition.
	Code string
	Err  error
}

// Error returns the string representation of the CodeError.
func (e *CodeError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Code, e.Err)
	}
	return e.Code
}

// Unwrap returns the underlying error, if any.
func (e *CodeError) Unwrap() error {
	return e.Err
}

// CheckGreen verifies that the git diff contains at least one changed source
// file within the given scope before allowing a ticket to turn green.
// A file is considered in scope if it has the prefix scope+"/" and does not
// end with ".md". If no such file exists, a *CodeError with Code "M1771" is returned.
// If the provider returns an error, it is wrapped in the CodeError.
func CheckGreen(scope string, d DiffProvider) error {
	files, err := d.ChangedFiles()
	if err != nil {
		return &CodeError{Code: "M1771", Err: fmt.Errorf("provider error: %w", err)}
	}

	prefix := scope + "/"
	for _, f := range files {
		if strings.HasPrefix(f, prefix) && !strings.HasSuffix(f, ".md") {
			return nil
		}
	}

	return &CodeError{Code: "M1771", Err: errors.New("no scoped source file change found")}
}
