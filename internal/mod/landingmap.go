// Package mod provides module resolution utilities.
package mod

import "errors"

// LandingResolver is an interface for looking up module entries by name.
type LandingResolver interface {
	Lookup(name string) (string, bool)
}

// ErrMissingLanding is returned when a landing entry is missing.
var ErrMissingLanding = errors.New("landing entry missing")

// ErrConflictingLanding is returned when landing targets conflict.
var ErrConflictingLanding = errors.New("landing target conflicts")

// CheckLanding checks that both modulesTable and modulesMap resolve "Landing" to "Crunch".
// For each non-nil input it calls Lookup("Landing") exactly once, modulesTable first then modulesMap.
// Both must return ("Crunch", true) to return nil.
func CheckLanding(modulesTable, modulesMap LandingResolver) error {
	var tVal, mVal string
	var tOk, mOk bool

	if modulesTable != nil {
		tVal, tOk = modulesTable.Lookup("Landing")
	}
	if modulesMap != nil {
		mVal, mOk = modulesMap.Lookup("Landing")
	}

	if modulesTable != nil && modulesMap != nil {
		if !tOk || !mOk {
			return ErrMissingLanding
		}
		if tVal != mVal {
			return ErrConflictingLanding
		}
		if tVal != "Crunch" {
			return ErrMissingLanding
		}
		return nil
	}

	if modulesTable != nil {
		if !tOk || tVal != "Crunch" {
			return ErrMissingLanding
		}
		return nil
	}

	if modulesMap != nil {
		if !mOk || mVal != "Crunch" {
			return ErrMissingLanding
		}
		return nil
	}

	return ErrMissingLanding
}
