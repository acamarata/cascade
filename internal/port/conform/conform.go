// Package conform holds the Port conformance suite. Each Port ships its
// cases with the Port; swarms build against the fake and prove the real
// module with this suite.
package conform

import "errors"

// ErrNotImplemented is returned by conformance stubs that have no logic yet.
var ErrNotImplemented = errors.New("conform: operation unavailable")

// Suite runs the conformance cases a Port ships against an implementation.
type Suite struct{}

// Run executes the suite. The stub always returns ErrNotImplemented.
func (s *Suite) Run() error {
	return ErrNotImplemented
}
