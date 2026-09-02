// Package fixture is the seeded-violation fixture for the internal/build
// arch test that proves cmd/** is the sole composition root: no directory
// other than cmd/** or internal/** itself may import internal/**
// (12-QUALITY-CONSTITUTION.md Art.10.2). This fixture models a hypothetical
// apps/ consumer reaching into internal/ directly, which the rule forbids
// regardless of which non-cmd/non-internal tree does it.
//
// Seeded violation — do not "fix" this file; it is the fixture.
package fixture

import (
	_ "github.com/acamarata/cascade/internal/storage"
)
