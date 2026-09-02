// Seeded-violation fixture (R-14.120(a), EVASION-1 — import alias) for the
// internal/build boundary lint. Lives under testdata/ so the Go toolchain
// never compiles it into the product (Art.7); it exists solely so
// TestBoundaryLint_SeededViolation_ImportAlias has a real
// aliased-fmt.Errorf boundary to fail on.
package boundaryfixture

import (
	ferrors "fmt"
)

// AliasedErrorf intentionally returns a raw error built through an aliased
// import of "fmt" (`import ferrors "fmt"`), which defeats a literal
// `fmt.Errorf` selector match. Seeded violation — do not "fix" this file;
// it is the fixture.
func AliasedErrorf(name string) error {
	return ferrors.Errorf("seeded boundary violation (aliased fmt): %q not found", name)
}
