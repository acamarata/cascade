// Package archive provides target policy definitions and parsing for archive operations.
package archive

import "errors"

// TargetPolicy describes an allowed set of types for a given target path.
type TargetPolicy struct {
	Target       string
	AllowedTypes []string
}

// ErrNotImplemented is returned when an archive operation is not yet available.
var ErrNotImplemented = errors.New("archive: operation unavailable")

// ParsePolicies parses raw policy data into a slice of TargetPolicy values.
// It currently returns nil, ErrNotImplemented as a stub implementation.
func ParsePolicies(_ map[string][]string) ([]TargetPolicy, error) {
	return nil, ErrNotImplemented
}
