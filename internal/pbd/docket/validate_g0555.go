package docket

import (
	"errors"
	"regexp"
)

// Resources is a slice of symbolic resource identifiers.
type Resources []string

// LaneCaps describes the credential capabilities permitted in a lane.
type LaneCaps struct {
	CredClasses []string
}

// ErrInvalidResources is returned when resources or lane caps are invalid.
var ErrInvalidResources = errors.New("D4555")

var resourceRe = regexp.MustCompile(`^(tool|node|fixture|cred):[a-z][a-z0-9_-]*$`)
var capRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// ValidateResources checks that every resource matches the allowed syntax
// and that credentials are present in lane.CredClasses. Nil or empty
// resources return nil if lane capabilities themselves validate.
func ValidateResources(resources Resources, lane LaneCaps) error {
	seen := make(map[string]bool)
	for _, r := range resources {
		if !resourceRe.MatchString(r) {
			return ErrInvalidResources
		}
		if seen[r] {
			return ErrInvalidResources
		}
		seen[r] = true
	}

	capSeen := make(map[string]bool)
	for _, c := range lane.CredClasses {
		if !capRe.MatchString(c) {
			return ErrInvalidResources
		}
		if capSeen[c] {
			return ErrInvalidResources
		}
		capSeen[c] = true
	}

	for _, r := range resources {
		if len(r) > 5 && r[:5] == "cred:" {
			name := r[5:]
			if !capSeen[name] {
				return ErrInvalidResources
			}
		}
	}

	return nil
}
