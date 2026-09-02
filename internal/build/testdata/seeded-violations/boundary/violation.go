// Package boundaryfixture is the seeded-violation fixture for the
// internal/build boundary lint (P1-E01-W1-S01-T7). It lives under testdata/
// so the Go toolchain never compiles it into the product (Art.7); it exists
// solely so TestBoundaryLint_SeededViolation has a real raw-error boundary
// to fail on, proving the lint's failing case actually fails.
//
// Both raw-error-constructor forms the lint watches for are represented
// here on purpose.
package boundaryfixture

import (
	"errors"
	"fmt"
)

// RawErrorf intentionally returns a raw fmt.Errorf value from what the lint
// treats as an exported API boundary, instead of a pkg/cascade taxonomy
// error. Seeded violation — do not "fix" this file; it is the fixture.
func RawErrorf(name string) error {
	return fmt.Errorf("seeded boundary violation: %q not found", name)
}

// RawErrorsNew intentionally returns a raw errors.New value from what the
// lint treats as an exported API boundary, instead of a pkg/cascade taxonomy
// error. Seeded violation — do not "fix" this file; it is the fixture.
func RawErrorsNew() error {
	return errors.New("seeded boundary violation: raw errors.New")
}
