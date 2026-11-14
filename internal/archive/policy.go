// Package archive provides target policy definitions and parsing for archive operations.
package archive

// TargetPolicy describes an allowed set of types for a given target path.
type TargetPolicy struct {
	Target       string
	AllowedTypes []string
}
