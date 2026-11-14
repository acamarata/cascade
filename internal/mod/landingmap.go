// Package mod provides module resolution utilities.
package mod

import (
	"errors"
	"reflect"
)

// LandingResolver is an interface for looking up module entries by name.
type LandingResolver interface {
	Lookup(name string) (string, bool)
}

// ErrMissingLanding is returned when a landing entry is missing.
var ErrMissingLanding = errors.New("landing entry missing")

// ErrConflictingLanding is returned when landing targets conflict.
var ErrConflictingLanding = errors.New("landing target conflicts")

// isAbsent reports whether r should be treated as absent.
// A nil interface, a typed nil of a nilable kind detected via reflect,
// or any other condition that makes Lookup unsafe returns true.
func isAbsent(r LandingResolver) bool {
	if r == nil {
		return true
	}
	v := reflect.ValueOf(r)
	if nillableKind[v.Kind()] {
		return v.IsNil()
	}
	return false
}

// nillableKind holds the reflect kinds whose zero value is nil, the only kinds IsNil may be called on.
var nillableKind = map[reflect.Kind]bool{
	reflect.Pointer: true, reflect.Interface: true, reflect.Map: true,
	reflect.Slice: true, reflect.Chan: true, reflect.Func: true,
}

// CheckLanding checks that both modulesTable and modulesMap resolve "Landing" to "Crunch".
// Absence in either input (nil interface, typed nil, or Lookup returning ok=false)
// takes precedence over conflicting values and returns ErrMissingLanding.
// A present result other than exactly "Crunch" returns ErrConflictingLanding,
// even when both registries agree on that wrong target.
// Neither registry is mutated. Lookup is never invoked on an absent resolver.
func CheckLanding(modulesTable, modulesMap LandingResolver) error {
	tAbsent := isAbsent(modulesTable)
	mAbsent := isAbsent(modulesMap)

	var tVal, mVal string
	var tOk, mOk bool

	if !tAbsent {
		tVal, tOk = modulesTable.Lookup("Landing")
	} else {
		tOk = false
	}

	if !mAbsent {
		mVal, mOk = modulesMap.Lookup("Landing")
	} else {
		mOk = false
	}

	// Missing takes precedence over conflict.
	if !tOk || !mOk {
		return ErrMissingLanding
	}

	// Both present: they must agree and the value must be exactly "Crunch".
	if tVal != mVal {
		return ErrConflictingLanding
	}

	if tVal != "Crunch" {
		return ErrConflictingLanding
	}

	return nil
}
