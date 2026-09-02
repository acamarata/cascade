// Package clockimpl is a legitimate-use fixture (see badtime/violation.go
// for the fixture-data discipline this mirrors). lintwall_test.go copies
// this source to a path ending in "internal/runtime/clock.go" inside a
// disposable temp module — the exact path .golangci.yml's forbidigo
// exclusion names — to prove the rule does NOT fire on the clock
// implementation itself, only on callers.
package clockimpl

import "time"

// SystemClock is a stand-in for internal/runtime's real SystemClock: the
// one place bare time.Now() is legitimate, because it IS the Clock
// abstraction domain code is supposed to depend on instead.
type SystemClock struct{}

// Now returns time.Now(). This is the rule's sanctioned exemption, not a
// violation of it.
func (SystemClock) Now() time.Time { return time.Now() }
