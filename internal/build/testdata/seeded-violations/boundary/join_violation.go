// Seeded-violation fixture (R-14.120(b), EVASION-2 — errors.Join) for the
// internal/build boundary lint. Lives under testdata/ so the Go toolchain
// never compiles it into the product (Art.7); it exists solely so
// TestBoundaryLint_SeededViolation_ErrorsJoin has a real errors.Join
// boundary to fail on.
package boundaryfixture

import (
	"errors"
)

// JoinedErrors intentionally returns a raw error built with errors.Join,
// a third raw-error constructor the original lint never checked for.
// Seeded violation — do not "fix" this file; it is the fixture.
func JoinedErrors(a, b error) error {
	return errors.Join(a, b)
}
