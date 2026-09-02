// Seeded-violation fixture (R-14.120(a), EVASION-1 — dot-import) for the
// internal/build boundary lint. Lives under testdata/ so the Go toolchain
// never compiles it into the product (Art.7); it exists solely so
// TestBoundaryLint_SeededViolation_DotImport has a real dot-imported
// "errors" boundary to fail on.
package boundaryfixture

import (
	. "errors"
)

// DotImportedNew intentionally dot-imports "errors" and calls New(...)
// unqualified, which is not even a *ast.SelectorExpr and so cannot be
// caught by a selector match on "errors.New" at all — the dot-import
// declaration itself must be rejected outright. Seeded violation — do not
// "fix" this file; it is the fixture.
func DotImportedNew() error {
	return New("seeded boundary violation: dot-imported errors.New")
}
