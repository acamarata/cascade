// Package fixture is the seeded-violation fixture for the internal/build
// arch test that proves plugins/** (and providers/**) may only import
// pkg/**, never internal/** (12-QUALITY-CONSTITUTION.md Art.10.2). It lives
// under testdata/ so the Go toolchain never compiles it into the product
// (Art.1/Art.7.1); the arch test materializes it into t.TempDir() and
// parses its import declaration directly — it never needs to resolve or
// build.
//
// Seeded violation — do not "fix" this file; it is the fixture.
package fixture

import (
	_ "github.com/acamarata/cascade/internal/runtime"
)
