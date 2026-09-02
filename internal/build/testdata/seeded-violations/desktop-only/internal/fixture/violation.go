// Package fixture is the seeded-violation fixture for the internal/build
// no-desktop-only lint: it imports a GUI-toolkit package from an
// unconstrained (non build-tagged) file, which the lint forbids everywhere
// under internal/ — Cascade's core is headless and product-agnostic, and
// this repo carries no UI (ASI Policy 2, PRI hard rule 1). It lives under
// testdata/ so the Go toolchain never compiles it into the product
// (Art.1/Art.7.1).
//
// Seeded violation — do not "fix" this file; it is the fixture.
package fixture

import (
	_ "fyne.io/fyne/v2/app"
)
