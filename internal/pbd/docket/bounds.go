// Package docket provides LeanBrief types and upper-bound validators per D1441.
package docket

import "errors"

// ErrStatusTooLong is returned when a status string exceeds 255 bytes.
var ErrStatusTooLong = errors.New("D1441: status exceeds 255 bytes")

// ErrSizeTooLarge is returned when a size value exceeds 10000.
var ErrSizeTooLarge = errors.New("D1441: size exceeds 10000")

// ErrTooManyDeps is returned when dependencies exceed 64.
var ErrTooManyDeps = errors.New("D1441: dependencies exceed 64")

// LeanBrief represents a minimal brief with status, size, and dependencies.
type LeanBrief struct {
	Status string
	Size   int
	Deps   []string
}

// ValidateStatus returns ErrStatusTooLong if s exceeds 255 bytes, nil otherwise.
func ValidateStatus(s string) error {
	if len(s) > 255 {
		return ErrStatusTooLong
	}
	return nil
}

// ValidateSize returns ErrSizeTooLarge if n exceeds 10000, nil otherwise.
func ValidateSize(n int) error {
	if n > 10000 {
		return ErrSizeTooLarge
	}
	return nil
}

// ValidateDeps returns ErrTooManyDeps if deps has more than 64 elements, nil otherwise.
// It does not check for blank or duplicate strings; content validation is out of scope.
func ValidateDeps(deps []string) error {
	if len(deps) > 64 {
		return ErrTooManyDeps
	}
	return nil
}

// Validate checks Status, then Size, then Deps, returning the first error or nil.
func Validate(b LeanBrief) error {
	if err := ValidateStatus(b.Status); err != nil {
		return err
	}
	if err := ValidateSize(b.Size); err != nil {
		return err
	}
	if err := ValidateDeps(b.Deps); err != nil {
		return err
	}
	return nil
}
